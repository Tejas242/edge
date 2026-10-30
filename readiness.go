package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"
	gosync "sync"
	"time"
)

// Readiness is the Trial 2 model: per-pattern skill and memory, the drill and
// the mistake log. v2/readiness owns this file.
//
// The model is a pure function of the history (CFDone, edge's tasks, the
// cached problem lists and the day), so it is rebuilt, not stored: only what
// can't be derived lives here.
type Readiness struct {
	Pick       *readyPick `json:"pick,omitempty"`        // why the current task was picked (Trial 2's picker)
	Mistakes   []Mistake  `json:"mistakes,omitempty"`    // what each rejected verdict was, as he said
	Drills     []Drill    `json:"drills,omitempty"`      // the weekly interview drills
	DrillWeek  string     `json:"drill_week,omitempty"`  // the week (Monday) DrillAt was rolled for
	DrillAt    time.Time  `json:"drill_at,omitempty"`    // this week's random moment
	DrillLater time.Time  `json:"drill_later,omitempty"` // "In 30 min" asks again then
}

// readyPick is what the Trial 2 picker chose and why.
type readyPick struct {
	Task    string  `json:"task"`
	Pattern string  `json:"pattern"`
	P       float64 `json:"p"`   // predicted chance of solving it solo in 45 minutes
	Why     string  `json:"why"` // one plain line for the dashboard
}

// Mistake is his answer to "What was it?" after a rejected verdict.
type Mistake struct {
	At      time.Time `json:"at"`
	Task    string    `json:"task"`
	Fail    int       `json:"fail"` // the task's rejected verdicts then: which one this was
	Verdict string    `json:"verdict,omitempty"`
	Kind    string    `json:"kind"` // misread | edge | overflow | approach | bug
}

// Drill is one weekly interview drill: the prediction made when it started.
type Drill struct {
	Week    string    `json:"week"` // its Monday
	Task    string    `json:"task"`
	Pattern string    `json:"pattern"`
	Rating  int       `json:"rating"` // Codeforces scale
	P       float64   `json:"p"`      // predicted chance of an AC within 45 minutes
	At      time.Time `json:"at"`
}

// The model's constants. Skill θ lives on the Codeforces rating scale, where
// a problem rated d is solved with P = 1/(1+10^((d−θ)/400)) (Elo/IRT).
const (
	readyModelVersion = 1
	interviewDefault  = 1400 // Codeforces scale: LeetCode ~1800, an upper medium, the onsite band
	readyTarget       = 0.75 // the readiness worth aiming each pattern at
	interviewTime     = 45 * time.Minute
	halfLifeStart     = 10.0  // days of memory to start with
	halfLifeMax       = 180.0 // a skill drilled for years still fades, slowly
	halfLifeMin       = 2.0
	spacedDays        = 2   // a success at least this long after the last one is spaced practice: ×2.5
	resolveBelow      = 0.8 // memory under this brings a cold re-solve (Trial 2)
	pickP             = 0.70
	drillP            = 0.60
	mcDraws           = 2000
	seedQ             = 0.85   // the upper quantile of solved ratings a pattern's θ starts at
	z80               = 1.2816 // the 10th/90th percentile of a normal: an 80% interval
	q400              = math.Ln10 / 400
)

var mistakeKinds = []action{{"misread", "Misread"}, {"edge", "Edge case"}, {"overflow", "Overflow"}, {"approach", "Approach"}, {"bug", "Bug"}}

// ---- config: [patterns] and [readiness] ----

// readyConfig reads a [patterns] weight or a [readiness] key; false = a bad
// line (the caller reports it).
func readyConfig(c *Config, section, k, v string) bool {
	switch section {
	case "patterns":
		w, err := strconv.ParseFloat(v, 64)
		if err != nil || w < 0 || w > 100 || patternName(k) == "" {
			return false
		}
		if c.Patterns == nil {
			c.Patterns = map[string]float64{}
		}
		c.Patterns[k] = w
		return true
	case "readiness":
		if k != "interview_rating" {
			return false
		}
		x, err := strconv.Atoi(v)
		if err != nil || x < 800 || x > 3000 {
			return false
		}
		c.Interview = x
		return true
	}
	return false
}

func patternWeight(id string) float64 {
	if w, ok := cfg().Patterns[id]; ok {
		return w
	}
	return 1
}

func interviewRating() float64 {
	if r := cfg().Interview; r > 0 {
		return float64(r)
	}
	return interviewDefault
}

// ---- the model ----

func solveP(theta, d float64) float64 { return 1 / (1 + math.Pow(10, (d-theta)/400)) }

// skill is one pattern's state: θ ± σ, and memory (half-life h days since
// the last success).
type skill struct {
	Theta, Sigma, H float64
	Last            time.Time
	Solved          int
}

// memory r = 2^(−t/h), t in whole edge days since the last success, so it
// moves once a day, not every tick. Never practised: 0.
func (k *skill) memory(at time.Time) float64 {
	if k.Last.IsZero() {
		return 0
	}
	t := math.Round(day(at).Sub(day(k.Last)).Hours() / 24)
	return math.Pow(2, -max(0, t)/k.H)
}

// succeed and fail move memory: a spaced success stretches the half-life
// ×2.5, a fail halves it.
func (k *skill) succeed(at time.Time) {
	if !k.Last.IsZero() && day(at).Sub(day(k.Last)) >= spacedDays*24*time.Hour {
		k.H = min(halfLifeMax, k.H*2.5)
	}
	if at.After(k.Last) {
		k.Last = at
	}
	k.Solved++
}

func (k *skill) fail() { k.H = max(halfLifeMin, k.H/2) }

// update is one result: θ += K·(score − P), K = q·σ² (Glicko's step, kept
// within 24..160) shared out over the m patterns the problem has; σ
// shrinks by the result's information, never below 60.
func (k *skill) update(d, score float64, m int) {
	p := solveP(k.Theta, d)
	K := min(160, max(24, q400*k.Sigma*k.Sigma)) / math.Sqrt(float64(m))
	k.Theta += K * (score - p)
	k.Sigma = max(60, 1/math.Sqrt(1/(k.Sigma*k.Sigma)+q400*q400*p*(1-p)))
}

// taskScore is a finished task as evidence: 1 for an AC in time with no
// help, ½ late or helped, 0 for a give-up or a skip after real work.
// Problems seen before, and skips before work, say nothing.
func taskScore(t Task) (float64, bool) {
	if t.Finished.IsZero() || t.Known {
		return 0, false
	}
	switch t.Outcome {
	case "solo":
		if t.Worked() <= interviewTime {
			return 1, true
		}
		return 0.5, true
	case "assisted":
		return 0.5, true
	case "gaveup":
		return 0, true
	case "skipped", "ignored":
		if t.Worked() >= skipWithin {
			return 0, true
		}
	}
	return 0, false
}

// taskMeta is a task's difficulty on the CF scale and its patterns, from
// the task or the problem lists.
func taskMeta(t Task, cat map[string]Problem) (float64, []string) {
	rating, tags := t.Rating, t.Tags
	if p, ok := cat[t.ID]; ok {
		if rating == 0 {
			rating = p.Rating
		}
		if len(tags) == 0 {
			tags = p.Tags
		}
	}
	return float64(cfScale(t.Source(), rating)), patternsOf(t.Source(), tags)
}

func quantile(xs []float64, q float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	x := q * float64(len(s)-1)
	i := int(x)
	if i+1 >= len(s) {
		return s[len(s)-1]
	}
	return s[i] + (x-float64(i))*(s[i+1]-s[i])
}

// skills seeds each pattern from his Codeforces history, then replays
// edge's own results in order.
//
// Seed: θ is the 85th percentile of the ratings he has solved in the
// pattern (solved problems undersell the best he can do, the very top is
// noise), shrunk towards his overall 85th percentile by 3 pseudo-solves.
// 0.85 is calibrated on his data: it puts greedy, his 116-solve bulk, at
// 1207 against a live rating of 1208 (0.75 gave 1126, 0.9 gave 1289).
// σ = 350/√(1+n/3), at least 100. Memory replays every solve's date as a
// success.
func (s *State) skills(cat map[string]Problem) map[string]*skill {
	inHist := map[string]bool{}
	for _, t := range s.History {
		inHist[t.ID] = true
	}
	type solve struct {
		at time.Time
		d  float64
	}
	by := map[string][]solve{}
	var all []float64
	for id, ts := range s.CFDone {
		p, ok := cat[id]
		if !ok || inHist[id] || p.Rating <= 0 {
			continue
		}
		all = append(all, float64(p.Rating))
		for _, pt := range patternsOf("cf", p.Tags) {
			by[pt] = append(by[pt], solve{time.Unix(ts, 0), float64(p.Rating)})
		}
	}
	prior := 1200.0
	if r := s.Ratings["cf"]; r > 0 {
		prior = float64(r)
	}
	if len(all) > 0 {
		prior = quantile(all, seedQ)
	}
	sk := map[string]*skill{}
	for _, p := range patterns {
		xs := by[p.ID]
		k := &skill{Theta: prior, Sigma: 350, H: halfLifeStart}
		if n := float64(len(xs)); n > 0 {
			ds := make([]float64, len(xs))
			for i, x := range xs {
				ds[i] = x.d
			}
			k.Theta = (n*quantile(ds, seedQ) + 3*prior) / (n + 3)
			k.Sigma = max(100, 350/math.Sqrt(1+n/3))
			slices.SortFunc(xs, func(a, b solve) int { return a.at.Compare(b.at) })
			for _, x := range xs {
				k.succeed(x.at)
			}
		}
		sk[p.ID] = k
	}
	hist := slices.Clone(s.History)
	slices.SortStableFunc(hist, func(a, b Task) int { return a.Finished.Compare(b.Finished) })
	for _, t := range hist {
		score, ok := taskScore(t)
		if !ok {
			continue
		}
		d, pats := taskMeta(t, cat)
		for _, p := range pats {
			k := sk[p]
			if d > 0 {
				k.update(d, score, len(pats))
			}
			if score > 0 {
				k.succeed(t.Finished)
			} else {
				k.fail()
			}
		}
	}
	return sk
}

// ReadyPattern is one pattern in the stats. p is the chance of solving an
// interview-level problem in it now, r the memory, ready = p·r; lo..hi is
// ready's 80% interval.
type ReadyPattern struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Theta    int     `json:"theta"`
	Sigma    int     `json:"sigma"`
	P        float64 `json:"p"`
	R        float64 `json:"r"`
	Ready    float64 `json:"ready"`
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	Solved   int     `json:"solved"`
	Last     string  `json:"last,omitempty"` // date of the last success
	HalfLife float64 `json:"half_life_days"`
}

// readyModel is the model evaluated on one day.
type readyModel struct {
	skill       map[string]*skill
	pats        []ReadyPattern
	R, Lo, Hi   float64 // readiness and its 80% interval, 0..1
	interview   float64
	byID        map[string]*ReadyPattern
	cat         map[string]Problem
	evaluatedAt time.Time
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// evaluate computes each pattern's p, r and interval, and readiness R =
// Σ w·P(d_interview)·r / Σ w with its 80% interval by Monte Carlo over
// θ ~ N(θ, σ). The draws use a fixed seed: the same inputs give the same
// numbers, so the stats change only when something real does.
func evaluate(sk map[string]*skill, at time.Time) *readyModel {
	m := &readyModel{skill: sk, interview: interviewRating(), byID: map[string]*ReadyPattern{}, evaluatedAt: at}
	type term struct{ w, theta, sigma, r float64 }
	var terms []term
	var W float64
	for _, p := range patterns {
		k := sk[p.ID]
		w := patternWeight(p.ID)
		r := k.memory(at)
		P := solveP(k.Theta, m.interview)
		x := ReadyPattern{ID: p.ID, Name: p.Name, Weight: w, Theta: int(math.Round(k.Theta)), Sigma: int(math.Round(k.Sigma)),
			P: round3(P), R: round3(r), Ready: round3(P * r), Solved: k.Solved, HalfLife: math.Round(k.H*10) / 10,
			Lo: round3(solveP(k.Theta-z80*k.Sigma, m.interview) * r), Hi: round3(solveP(k.Theta+z80*k.Sigma, m.interview) * r)}
		if !k.Last.IsZero() {
			x.Last = day(k.Last).Format(time.DateOnly)
		}
		m.pats = append(m.pats, x)
		W += w
		if w > 0 && r > 0 {
			terms = append(terms, term{w, k.Theta, k.Sigma, r})
		}
	}
	for i := range m.pats {
		m.byID[m.pats[i].ID] = &m.pats[i]
	}
	if W <= 0 {
		return m
	}
	rng := rand.New(rand.NewPCG(20261120, 2))
	draws := make([]float64, mcDraws)
	var sum float64
	for i := range draws {
		var x float64
		for _, t := range terms {
			x += t.w * solveP(t.theta+t.sigma*rng.NormFloat64(), m.interview) * t.r
		}
		draws[i] = x / W
		sum += draws[i]
	}
	slices.Sort(draws)
	m.R, m.Lo, m.Hi = sum/mcDraws, draws[mcDraws/10], draws[mcDraws*9/10]
	return m
}

// The model is cached: rebuilt only when the history, the problem lists,
// the config or the day changes. stats() runs every tick.
var readyMemo struct {
	mu  gosync.Mutex
	key string
	m   *readyModel
}

func (s *State) model() *readyModel {
	cat, stamp := readyCatalog()
	var last int64
	if n := len(s.History); n > 0 {
		last = s.History[n-1].Finished.UnixNano()
	}
	ws := make([]string, 0, len(patterns))
	for _, p := range patterns {
		ws = append(ws, strconv.FormatFloat(patternWeight(p.ID), 'g', -1, 64))
	}
	key := fmt.Sprint(len(s.History), last, len(s.CFDone), s.Ratings["cf"], day(now()).Unix(), interviewRating(), stamp, strings.Join(ws, ","))
	readyMemo.mu.Lock()
	defer readyMemo.mu.Unlock()
	if readyMemo.m != nil && readyMemo.key == key {
		return readyMemo.m
	}
	m := evaluate(s.skills(cat), now())
	m.cat = cat
	readyMemo.key, readyMemo.m = key, m
	return m
}

func pct(x float64) int { return int(math.Round(x * 100)) }

// ---- stats ----

// ReadinessStats is its part of `edge stats`. It runs in shadow from the
// start; Ember shows it from Trial 2.
type ReadinessStats struct {
	Model     int            `json:"model"`            // model version
	Interview int            `json:"interview_rating"` // the interview problem's difficulty, Codeforces scale
	LCOffset  int            `json:"lc_offset"`        // LeetCode (zerotrac) rating − this = Codeforces scale
	R         float64        `json:"r"`                // readiness now, 0..1
	Lo        float64        `json:"lo"`               // its 80% interval
	Hi        float64        `json:"hi"`
	Target    float64        `json:"target"` // what each pattern's ready aims at (0..1)
	Patterns  []ReadyPattern `json:"patterns"`
	Pick      *readyPick     `json:"pick,omitempty"` // why the current task, when Trial 2 picked it
	Mistakes  MistakeStats   `json:"mistakes"`
	Drills    []DrillStat    `json:"drills,omitempty"`
	Brier     *float64       `json:"brier,omitempty"` // mean (predicted − actual)² over finished drills
}

// MistakeStats counts his answers by kind.
type MistakeStats struct {
	Total  int            `json:"total"`
	All    map[string]int `json:"all,omitempty"`
	Recent map[string]int `json:"last_30d,omitempty"`
	Top    string         `json:"top,omitempty"` // the most common in the last 30 days (else all time)
}

// DrillStat is a drill with its result: pending | ac | missed.
type DrillStat struct {
	Week    string  `json:"week"`
	Task    string  `json:"task"`
	Pattern string  `json:"pattern"`
	Rating  int     `json:"rating"`
	P       float64 `json:"p"`
	Result  string  `json:"result"`
}

func (s *State) readinessStats() *ReadinessStats {
	m := s.model()
	st := &ReadinessStats{Model: readyModelVersion, Interview: int(m.interview), LCOffset: lcOffset,
		R: round3(m.R), Lo: round3(m.Lo), Hi: round3(m.Hi), Target: readyTarget, Patterns: m.pats}
	if p := s.Ready.Pick; p != nil && s.Current != nil && s.Current.ID == p.Task {
		st.Pick = p
	}
	st.Mistakes = s.mistakeStats()
	var sq float64
	var n int
	for _, d := range s.Ready.Drills {
		x := DrillStat{Week: d.Week, Task: d.Task, Pattern: d.Pattern, Rating: d.Rating, P: d.P, Result: "pending"}
		if o, ok := s.drillResult(d); ok {
			x.Result = "missed"
			if o == 1 {
				x.Result = "ac"
			}
			sq += (d.P - o) * (d.P - o)
			n++
		}
		st.Drills = append(st.Drills, x)
	}
	if n > 0 {
		b := round3(sq / float64(n))
		st.Brier = &b
	}
	return st
}

// drillResult: 1 for an AC within 45 minutes of work with no help, else 0;
// ok=false while it's still going.
func (s *State) drillResult(d Drill) (float64, bool) {
	for i := len(s.History) - 1; i >= 0; i-- {
		if t := s.History[i]; t.ID == d.Task && t.Kind == "drill" && !t.Finished.IsZero() {
			if t.Outcome == "solo" && t.Worked() <= interviewTime {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}

func (s *State) mistakeStats() MistakeStats {
	ms := MistakeStats{All: map[string]int{}, Recent: map[string]int{}}
	from := now().AddDate(0, 0, -30)
	for _, x := range s.Ready.Mistakes {
		ms.Total++
		ms.All[x.Kind]++
		if x.At.After(from) {
			ms.Recent[x.Kind]++
		}
	}
	top := func(c map[string]int) string {
		best := ""
		for _, k := range mistakeKinds { // fixed order: ties go to the first
			if c[k.Key] > c[best] {
				best = k.Key
			}
		}
		return best
	}
	if ms.Top = top(ms.Recent); ms.Top == "" {
		ms.Top = top(ms.All)
	}
	return ms
}

// ---- status ----

// taskPattern is the interview pattern a task exercises, by name: the one
// it was picked for, else its heaviest-weighted one. "" when its tags map
// to none.
func (s *State) taskPattern(t *Task) string {
	if t == nil {
		return ""
	}
	if p := s.Ready.Pick; p != nil && p.Task == t.ID {
		return patternName(p.Pattern)
	}
	cat, _ := readyCatalog()
	_, pats := taskMeta(*t, cat)
	best := ""
	for _, p := range pats {
		if best == "" || patternWeight(p) > patternWeight(best) {
			best = p
		}
	}
	return patternName(best)
}

// readinessStatus fills the status fields Trial 2 owns, from Trial 2 on.
func readinessStatus(s *State, st *emberStatus) {
	if s.phase(now()) != "trial2" {
		return
	}
	m := s.model()
	st.ReadyPct, st.ReadyLo, st.ReadyHi = pct(m.R), pct(m.Lo), pct(m.Hi)
	t := s.Current
	if t == nil {
		return
	}
	st.Pattern = s.taskPattern(t)
	if t.Kind == "drill" {
		st.Drill = true
		if st.DeadlineMs == 0 && t.Active() && t.Worked() < interviewTime {
			// the countdown's end: from now while working, else from the
			// last work, so an idle status stays byte-identical
			working, _ := workState(s, t)
			base := t.Started
			if t.WorkAt.After(base) {
				base = t.WorkAt
			}
			if working {
				base = now()
			}
			st.DeadlineMs = base.Add(interviewTime - t.Worked()).UnixMilli()
		}
	}
}

// ---- actions ----

// readinessAction handles Trial 2's action keys: mistake:<id>:<kind>,
// drill (start the drill now) and drill:later. handled=false leaves the key
// to others.
func readinessAction(s *State, verb, arg string) (handled bool, msg string, err error) {
	return false, "", nil
}

// ---- cards ----

// readinessEvents are this trial's cards for plan(): it checks its own phase.
func readinessEvents(s *State, w window, n time.Time) []event { return nil }

// sortedPatterns orders pattern ids by f, highest first (stable on the list order).
func sortedPatterns(f func(id string) float64) []string {
	ids := make([]string, len(patterns))
	for i, p := range patterns {
		ids[i] = p.ID
	}
	sort.SliceStable(ids, func(i, j int) bool { return f(ids[i]) > f(ids[j]) })
	return ids
}
