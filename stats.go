package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// Stats is edge's public read model: everything a dashboard needs, in one
// versioned JSON document. `edge stats` prints it; the daemon keeps
// $XDG_RUNTIME_DIR/edge/stats.json current and pushes it to Ember. Fields
// are only ever added within a version. See STATS.md.
//
// The current task's topic and tags are never in it: they stay hidden until
// the problem is done, as everywhere else in edge.
const statsVersion = 1

type Stats struct {
	Version    int                    `json:"version"`
	Generated  time.Time              `json:"generated"`
	Accounts   map[string]string      `json:"accounts"`
	Today      StatsToday             `json:"today"`
	Current    *StatsTask             `json:"current"`
	Streak     StatsStreak            `json:"streak"`
	Week       StatsWeek              `json:"week"`
	Calendar   []StatsDay             `json:"calendar"`
	Totals     StatsTotals            `json:"totals"`
	Sources    map[string]StatsSource `json:"sources"`
	Topics     []StatsTopic           `json:"topics"`
	Difficulty []StatsBucket          `json:"difficulty"`
	Resolves   StatsResolves          `json:"resolves"`
	Experiment StatsExperiment        `json:"experiment"`
	Recent     []StatsTask            `json:"recent"`
	Health     []StatsTrouble         `json:"health"`
	Daemon     StatsDaemon            `json:"daemon"`
	Rounds     StatsRounds            `json:"rounds"`
}

// StatsRounds is live contests: the plan, the one on now, how they went
// and what's owed.
type StatsRounds struct {
	Enabled  bool          `json:"enabled"`
	PerWeek  int           `json:"per_week"`
	Lock     bool          `json:"lock"`
	Live     *StatsRound   `json:"live"`     // on now and held (or joined); null otherwise
	Next     *StatsRound   `json:"next"`     // the next one they're held to; null if none
	Upcoming []StatsRound  `json:"upcoming"` // the next 8 days, soonest first, open ones too
	Recent   []StatsRound  `json:"recent"`   // finished rounds sat or held, newest first
	Upsolves StatsUpsolves `json:"upsolves"`
	Weeks    StatsStreak   `json:"streak_weeks"` // weeks in a row with a round sat
	Record   StatsRecord   `json:"record"`
}

type StatsRound struct {
	ID       string              `json:"id"`
	Judge    string              `json:"judge"` // cf | ac | lc
	Name     string              `json:"name"`
	Short    string              `json:"short"` // "CF 1128", "ABC 480", "Weekly 524"
	Kind     string              `json:"kind"`  // div2 div3 div4 edu div12 abc weekly biweekly
	URL      string              `json:"url"`
	Start    time.Time           `json:"start"`
	End      time.Time           `json:"end"`
	Plan     string              `json:"plan"`             // in | out | open
	Joined   string              `json:"joined,omitempty"` // live | virtual
	State    string              `json:"state"`            // upcoming | live | settling | over
	Solved   int                 `json:"solved"`
	Credit   int                 `json:"credit"`
	Rated    bool                `json:"rated,omitempty"`
	Rating   int                 `json:"rating,omitempty"`
	Delta    int                 `json:"delta,omitempty"`
	Lesson   string              `json:"lesson,omitempty"`
	Problems []StatsRoundProblem `json:"problems"`
}

type StatsRoundProblem struct {
	ID     string    `json:"id"`
	Index  string    `json:"index"`
	Title  string    `json:"title"`
	URL    string    `json:"url"`
	Rating int       `json:"rating,omitempty"`
	State  string    `json:"state"` // "" | tried | solved | upsolve | upsolved | taught | dropped | lapsed
	AtSec  int       `json:"at_sec,omitempty"`
	Fails  int       `json:"fails,omitempty"`
	Due    time.Time `json:"due,omitzero"`
}

type StatsUpsolves struct {
	Open   int            `json:"open"`
	Done   int            `json:"done"`   // solved after the round
	Taught int            `json:"taught"` // given up and taught
	Lapsed int            `json:"lapsed"`
	Queue  []StatsUpsolve `json:"queue"` // oldest round first; the first is offered next
}

type StatsUpsolve struct {
	ID     string    `json:"id"`
	Title  string    `json:"title"`
	URL    string    `json:"url"`
	Round  string    `json:"round"`
	Short  string    `json:"short"`
	Index  string    `json:"index"`
	Rating int       `json:"rating,omitempty"`
	Due    time.Time `json:"due"`
}

type StatsRecord struct {
	Held   int `json:"held"`   // finished rounds they were held to
	Sat    int `json:"sat"`    // of those, sat (live or virtually)
	Missed int `json:"missed"` // held and not sat, or "can't make it"
	Extra  int `json:"extra"`  // open rounds they sat anyway
}

type StatsToday struct {
	Date      string `json:"date"` // the edge day (days end at day_ends)
	Got       int    `json:"got"`
	Need      int    `json:"need"`
	Done      bool   `json:"done"`
	WorkSec   int    `json:"work_sec"`
	StudyTime bool   `json:"study_time"`
	StudyFrom string `json:"study_from"` // "19:00"
	DayEnds   string `json:"day_ends"`   // "04:00"
	Gate      string `json:"gate"`       // closed | open | off
	GateWhy   string `json:"gate_why"`   // done | pass | outside_study_time | not_enforcing | disabled | edge_trouble | ""
	PassUsed  bool   `json:"pass_used_this_week"`
}

type StatsTask struct {
	ID           string    `json:"id"`
	Source       string    `json:"source"` // cf | lc
	Kind         string    `json:"kind"`   // new | resolve | reentry
	Title        string    `json:"title"`
	URL          string    `json:"url"`
	Rating       int       `json:"rating"`
	Assigned     time.Time `json:"assigned"`
	Started      time.Time `json:"started,omitzero"`
	Finished     time.Time `json:"finished,omitzero"`
	Outcome      string    `json:"outcome,omitempty"` // solo | assisted | gaveup | skipped | ignored
	Known        bool      `json:"known,omitempty"`
	WorkSec      int       `json:"work_sec"`
	DriftSec     int       `json:"drift_sec"`
	Hints        int       `json:"hints"`
	HintsAllowed int       `json:"hints_allowed"`            // current task only
	NextHintSec  int       `json:"next_hint_sec,omitempty"`  // work until the next unlocks
	GiveUpInSec  int       `json:"give_up_in_sec,omitempty"` // work until giving up is allowed
	Fails        int       `json:"fails"`
	Explain      int       `json:"explain"`                 // 0..2, -1 = not done
	Credit       int       `json:"credit"`                  // what it earned for its day
	Focus        string    `json:"focus,omitempty"`         // finished tasks only
	Tags         []string  `json:"tags,omitempty"`          // finished tasks only
	Stage        int       `json:"resolve_stage,omitempty"` // re-solves
}

type StatsStreak struct {
	Current int `json:"current"`
	Best    int `json:"best"`
}

type StatsWeek struct {
	Start   string     `json:"start"` // Monday
	Credits int        `json:"credits"`
	Target  int        `json:"target"`
	Days    []StatsDay `json:"days"`
}

type StatsDay struct {
	Date     string `json:"date"`
	Credits  int    `json:"credits"`
	Need     int    `json:"need"`
	Met      bool   `json:"met"`
	Pass     bool   `json:"pass,omitempty"`
	WorkSec  int    `json:"work_sec"`
	CFSolved int    `json:"cf_solved"` // first accepts on Codeforces that day, edge or not
}

type StatsTotals struct {
	Tasks    int `json:"tasks"`
	Solved   int `json:"solved"`
	Solo     int `json:"solo"`
	Assisted int `json:"assisted"`
	GaveUp   int `json:"gave_up"`
	Skipped  int `json:"skipped"`
	Ignored  int `json:"ignored"`
	Known    int `json:"known"`
	WorkSec  int `json:"work_sec"`
	DriftSec int `json:"drift_sec"`
	Hints    int `json:"hints"`
	Fails    int `json:"fails"`
	Passes   int `json:"passes"`
	Days     int `json:"days_met"`
}

type StatsSource struct {
	Rating    int           `json:"rating"`
	BandLo    int           `json:"band_lo"`
	BandHi    int           `json:"band_hi"`
	Offset    int           `json:"offset"`
	SolvedAll int           `json:"solved_all_time,omitempty"` // CF: every accepted problem, edge or not
	History   []RatingPoint `json:"rating_history"`
	Synced    time.Time     `json:"synced,omitzero"`
}

type StatsTopic struct {
	Tag        string  `json:"tag"`
	Source     string  `json:"source"`
	Weight     float64 `json:"focus_weight"` // 0 = not a focus topic
	Tasks      int     `json:"tasks"`
	Solo       int     `json:"solo"`
	Assisted   int     `json:"assisted"`
	GaveUp     int     `json:"gave_up"`
	AvgWorkSec int     `json:"avg_work_sec"`
}

type StatsBucket struct {
	Source   string `json:"source"`
	Lo       int    `json:"lo"` // rating lo..lo+99
	Tasks    int    `json:"tasks"`
	Solo     int    `json:"solo"`
	Assisted int    `json:"assisted"`
	GaveUp   int    `json:"gave_up"`
}

type StatsResolves struct {
	Due       int             `json:"due"`
	Scheduled int             `json:"scheduled"`
	Graduated int             `json:"graduated"`
	Attempts  int             `json:"first_attempts"` // first re-solves: the retention measure
	Retained  int             `json:"first_retained"`
	Upcoming  []StatsUpcoming `json:"upcoming"`
}

type StatsUpcoming struct {
	ID     string    `json:"id"`
	Title  string    `json:"title"`
	URL    string    `json:"url"`
	Rating int       `json:"rating"`
	Due    time.Time `json:"due"`
	Stage  int       `json:"stage"`
	Try    int       `json:"try"`
}

type StatsExperiment struct {
	Status  string               `json:"status"` // running | decided
	Target  int                  `json:"target_per_arm"`
	Arms    map[string]*armStats `json:"arms"`
	Verdict string               `json:"verdict,omitempty"` // ladder | instant | none
	P       float64              `json:"p_ladder_better,omitempty"`
}

type StatsTrouble struct {
	Key   string    `json:"key"`
	Since time.Time `json:"since"`
	Msg   string    `json:"msg"`
}

type StatsDaemon struct {
	Alive bool      `json:"alive"`
	Seen  time.Time `json:"seen,omitzero"`
	Gate  time.Time `json:"gate_hook_seen,omitzero"`
}

const (
	calendarDays = 365
	recentTasks  = 30
	upcomingMax  = 20
)

func (s *State) stats() Stats {
	n := now()
	c := cfg()
	today := day(n)
	got, need := s.today()
	st := Stats{Version: statsVersion, Generated: n, Accounts: map[string]string{"cf": c.CFUser, "lc": c.LCUser}}

	// today and the gate
	from := c.WeekdayFrom
	if weekend(n) {
		from = c.WeekendFrom
	}
	st.Today = StatsToday{Date: today.Format(time.DateOnly), Got: got, Need: need, Done: got >= need,
		StudyTime: studyTime(n), StudyFrom: hm(from), DayEnds: hm(c.DayEnds), PassUsed: s.passUsedThisWeek()}
	switch {
	case !s.rules().Gate:
		st.Today.Gate, st.Today.GateWhy = "off", "disabled"
	case s.closed():
		st.Today.Gate = "closed"
	default:
		st.Today.Gate = "open"
		switch {
		case s.GateFrom.IsZero() || n.Before(s.GateFrom):
			st.Today.GateWhy = "not_enforcing"
		case got >= need:
			st.Today.GateWhy = "done"
		case s.passOn(n):
			st.Today.GateWhy = "pass"
		case s.failOpen() != "":
			st.Today.GateWhy = "edge_trouble"
		case s.roundToday() != nil:
			st.Today.GateWhy = "round_today"
		default:
			st.Today.GateWhy = "outside_study_time"
		}
	}
	if t := s.Current; t != nil {
		ct := taskStats(*t, false)
		ct.HintsAllowed, _ = hintAllowance(t)
		if _, next := hintAllowance(t); next > 0 {
			ct.NextHintSec = int(next.Seconds())
		}
		if ok, wait := canGiveUp(t); !ok && t.Active() {
			ct.GiveUpInSec = int(wait.Seconds())
		}
		st.Current = &ct
	}

	// days: credits, work, passes, CF first accepts
	type dayAgg struct{ credits, work, cf int }
	days := map[time.Time]*dayAgg{}
	at := func(d time.Time) *dayAgg {
		if days[d] == nil {
			days[d] = &dayAgg{}
		}
		return days[d]
	}
	for _, t := range s.History {
		if t.Finished.IsZero() {
			continue
		}
		a := at(day(t.Finished))
		a.credits += credit(t)
		a.work += t.WorkSec
	}
	for i := range s.Rounds {
		if r := &s.Rounds[i]; r.Joined != "" {
			a := at(day(r.creditAt()))
			a.credits += s.roundCredit(r)
			if r.Joined == "live" {
				end := r.End()
				if n.Before(end) {
					end = n
				}
				a.work += int(max(0, end.Sub(r.Start)).Seconds()) // a round is focused work
			}
		}
	}
	if t := s.Current; t != nil && t.WorkSec > 0 {
		at(today).work += t.WorkSec
	}
	for _, ts := range s.CFDone {
		at(day(time.Unix(ts, 0))).cf++
	}
	mkDay := func(d time.Time) StatsDay {
		a := days[d]
		if a == nil {
			a = &dayAgg{}
		}
		mid := d.Add(12 * time.Hour)
		need := s.dailyMin(mid)
		return StatsDay{Date: d.Format(time.DateOnly), Credits: a.credits, Need: need, Met: a.credits >= need && need > 0,
			Pass: s.passOn(mid), WorkSec: a.work, CFSolved: a.cf}
	}
	for i := calendarDays - 1; i >= 0; i-- {
		st.Calendar = append(st.Calendar, mkDay(today.AddDate(0, 0, -i)))
	}
	st.Today.WorkSec = st.Calendar[len(st.Calendar)-1].WorkSec
	ws := weekStart(n)
	st.Week = StatsWeek{Start: ws.Format(time.DateOnly), Credits: s.creditsSince(ws), Target: c.Weekly}
	for i := 0; i < 7; i++ {
		st.Week.Days = append(st.Week.Days, mkDay(ws.AddDate(0, 0, i)))
	}

	// streaks: current is edge's rule; best scans every day since the first task
	st.Streak.Current = s.streak()
	if len(s.History) > 0 || slices.ContainsFunc(s.Rounds, func(r Round) bool { return r.Joined != "" }) {
		first := today
		for _, t := range s.History {
			if !t.Finished.IsZero() && day(t.Finished).Before(first) {
				first = day(t.Finished)
			}
		}
		for i := range s.Rounds {
			if r := &s.Rounds[i]; r.Joined != "" && day(r.creditAt()).Before(first) {
				first = day(r.creditAt())
			}
		}
		run := 0
		for d := first; !d.After(today); d = d.AddDate(0, 0, 1) {
			sd := mkDay(d)
			switch {
			case sd.Met:
				run++
				st.Totals.Days++
			case sd.Pass, d.Equal(today):
			default:
				run = 0
			}
			st.Streak.Best = max(st.Streak.Best, run)
		}
	}
	st.Streak.Best = max(st.Streak.Best, st.Streak.Current)

	// totals, topics, difficulty, recent
	topics := map[[2]string]*StatsTopic{}
	buckets := map[[2]int]*StatsBucket{}
	srcIdx := map[string]int{"cf": 0, "lc": 1}
	for _, t := range s.History {
		tt := &st.Totals
		tt.Tasks++
		tt.WorkSec += t.WorkSec
		tt.DriftSec += t.DriftSec
		tt.Hints += t.Hints
		tt.Fails += t.Fails
		switch t.Outcome {
		case "solo":
			tt.Solo++
			tt.Solved++
		case "assisted":
			tt.Assisted++
			tt.Solved++
		case "gaveup":
			tt.GaveUp++
		case "skipped":
			tt.Skipped++
		case "ignored":
			tt.Ignored++
		}
		if t.Known {
			tt.Known++
		}
		if !attempted(t) {
			continue
		}
		for _, tag := range t.Tags {
			k := [2]string{t.Source(), tag}
			if topics[k] == nil {
				topics[k] = &StatsTopic{Tag: tag, Source: t.Source(), Weight: c.Focus[t.Source()][tag]}
			}
			countOutcome(t, &topics[k].Tasks, &topics[k].Solo, &topics[k].Assisted, &topics[k].GaveUp)
			topics[k].AvgWorkSec += t.WorkSec // summed; averaged below
		}
		if t.Kind == "new" && t.Rating > 0 {
			k := [2]int{srcIdx[t.Source()], t.Rating / 100 * 100}
			if buckets[k] == nil {
				buckets[k] = &StatsBucket{Source: t.Source(), Lo: k[1]}
			}
			b := buckets[k]
			countOutcome(t, &b.Tasks, &b.Solo, &b.Assisted, &b.GaveUp)
		}
	}
	st.Totals.Passes = len(s.Passes)
	for src, w := range c.Focus { // focus topics show up even before any task
		for tag, wt := range w {
			if k := [2]string{src, tag}; topics[k] == nil {
				topics[k] = &StatsTopic{Tag: tag, Source: src, Weight: wt}
			}
		}
	}
	for _, tp := range topics {
		if tp.Tasks > 0 {
			tp.AvgWorkSec /= tp.Tasks
		}
		st.Topics = append(st.Topics, *tp)
	}
	sort.Slice(st.Topics, func(i, j int) bool {
		a, b := st.Topics[i], st.Topics[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Tasks != b.Tasks {
			return a.Tasks > b.Tasks
		}
		return a.Tag < b.Tag
	})
	for _, b := range buckets {
		st.Difficulty = append(st.Difficulty, *b)
	}
	sort.Slice(st.Difficulty, func(i, j int) bool {
		a, b := st.Difficulty[i], st.Difficulty[j]
		return a.Source < b.Source || a.Source == b.Source && a.Lo < b.Lo
	})
	for i := len(s.History) - 1; i >= 0 && len(st.Recent) < recentTasks; i-- {
		st.Recent = append(st.Recent, taskStats(s.History[i], true))
	}

	// sources
	st.Sources = map[string]StatsSource{}
	for _, src := range []string{"cf", "lc"} {
		lo, hi := s.window(src)
		ss := StatsSource{Rating: s.Ratings[src], BandLo: lo, BandHi: hi, Offset: s.Offset[src],
			History: s.RatingLog[src]}
		if ss.History == nil {
			ss.History = []RatingPoint{}
		}
		if src == "cf" {
			ss.SolvedAll = len(s.CFDone)
		}
		if l := s.Sync[src]; l != nil {
			ss.Synced = l.OK
		}
		st.Sources[src] = ss
	}

	// re-solves
	r := &st.Resolves
	r.Scheduled = len(s.Probes)
	probes := slices.Clone(s.Probes)
	slices.SortFunc(probes, func(a, b Probe) int { return a.Due.Compare(b.Due) })
	for _, p := range probes {
		if !p.Due.After(n) {
			r.Due++
		}
		if len(r.Upcoming) < upcomingMax {
			r.Upcoming = append(r.Upcoming, StatsUpcoming{ID: p.Problem, Title: p.Title, URL: p.URL, Rating: p.Rating, Due: p.Due, Stage: p.Stage, Try: p.Try})
		}
	}
	counted := map[string]bool{}
	for _, t := range s.History {
		if t.Kind != "resolve" || t.Outcome == "" {
			continue
		}
		if t.ProbeStage >= 2 && t.Outcome == "solo" {
			r.Graduated++
		}
		if t.ProbeStage == 1 && t.ProbeTry == 0 && !counted[t.ID] {
			counted[t.ID] = true
			r.Attempts++
			if t.Outcome == "solo" {
				r.Retained++
			}
		}
	}

	// experiment
	arms := s.arms()
	e := &st.Experiment
	e.Target, e.Arms, e.Status = probesPerArm, arms, "running"
	if arms["ladder"].Probes >= probesPerArm && arms["instant"].Probes >= probesPerArm {
		e.Status = "decided"
		e.P = probBetter(s.firstPasses("ladder"), probesPerArm, s.firstPasses("instant"), probesPerArm)
		switch {
		case e.P >= winnerConf:
			e.Verdict = "ladder"
		case e.P <= 1-winnerConf:
			e.Verdict = "instant"
		default:
			e.Verdict = "none"
		}
	}

	// health
	for _, k := range slices.Sorted(mapsKeys(s.Trouble)) {
		st.Health = append(st.Health, StatsTrouble{Key: k, Since: s.Trouble[k].Since, Msg: s.Trouble[k].Msg})
	}
	seen := mtime(daemonSeenPath())
	st.Daemon = StatsDaemon{Alive: !seen.IsZero() && time.Since(seen) < 2*time.Minute, Seen: seen, Gate: gateSeen()}

	st.Rounds = s.roundStats()

	// empty lists are [] not null: easier for every consumer
	for _, l := range []*[]StatsDay{&st.Calendar, &st.Week.Days} {
		if *l == nil {
			*l = []StatsDay{}
		}
	}
	if st.Topics == nil {
		st.Topics = []StatsTopic{}
	}
	if st.Difficulty == nil {
		st.Difficulty = []StatsBucket{}
	}
	if st.Recent == nil {
		st.Recent = []StatsTask{}
	}
	if st.Health == nil {
		st.Health = []StatsTrouble{}
	}
	if r.Upcoming == nil {
		r.Upcoming = []StatsUpcoming{}
	}
	return st
}

func (s *State) roundStats() StatsRounds {
	c, n := cfg(), now()
	rs := StatsRounds{Enabled: c.Rounds, PerWeek: c.PerWeek, Lock: s.rules().RoundLock,
		Upcoming: []StatsRound{}, Recent: []StatsRound{}, Upsolves: StatsUpsolves{Queue: []StatsUpsolve{}}}
	export := func(r *Round) StatsRound {
		x := StatsRound{ID: r.ID, Judge: r.Judge(), Name: r.Name, Short: r.Short(), Kind: r.Kind, URL: r.RoundURL(),
			Start: r.Start, End: r.End(), Plan: r.Plan, Joined: r.Joined, Solved: r.solved(), Credit: s.roundCredit(r),
			Rated: r.Rated, Rating: r.Rating, Delta: r.Delta, Lesson: r.Lesson, Problems: []StatsRoundProblem{}}
		if x.Plan == "" {
			x.Plan = "open"
		}
		switch {
		case n.Before(r.Start):
			x.State = "upcoming"
		case r.Live(n):
			x.State = "live"
		case r.Settled.IsZero():
			x.State = "settling"
		default:
			x.State = "over"
		}
		for _, p := range r.Problems {
			x.Problems = append(x.Problems, StatsRoundProblem{ID: p.ID, Index: p.Index, Title: p.Title, URL: p.URL, Rating: p.Rating,
				State: p.State, AtSec: p.At, Fails: p.Fails, Due: p.Due})
		}
		return x
	}
	if r := s.liveHeld(); r != nil {
		x := export(r)
		rs.Live = &x
	}
	if r := s.nextHeld(); r != nil && n.Before(r.Start) {
		x := export(r)
		rs.Next = &x
	}
	weeks := map[time.Time]bool{}
	for i := range s.Rounds {
		r := &s.Rounds[i]
		if r.Gone {
			continue
		}
		if n.Before(r.End()) && r.Start.Sub(n) <= planAhead {
			rs.Upcoming = append(rs.Upcoming, export(r))
		}
		if r.Joined != "" {
			weeks[weekStart(r.creditAt())] = true
		}
		if !r.Settled.IsZero() {
			switch {
			case r.Plan == "in" && r.Joined != "":
				rs.Record.Held++
				rs.Record.Sat++
			case r.Plan != "" && r.Joined == "":
				rs.Record.Held++
				rs.Record.Missed++
			case r.Joined != "":
				rs.Record.Extra++
			}
		}
		for _, p := range r.Problems {
			switch p.State {
			case "upsolve":
				rs.Upsolves.Open++
				rs.Upsolves.Queue = append(rs.Upsolves.Queue, StatsUpsolve{ID: p.ID, Title: p.Title, URL: p.URL, Round: r.ID,
					Short: r.Short(), Index: p.Index, Rating: p.Rating, Due: p.Due})
			case "upsolved":
				rs.Upsolves.Done++
			case "taught":
				rs.Upsolves.Taught++
			case "lapsed":
				rs.Upsolves.Lapsed++
			}
		}
	}
	for i := len(s.Rounds) - 1; i >= 0 && len(rs.Recent) < 12; i-- {
		if r := &s.Rounds[i]; !n.Before(r.End()) && !r.Gone && (r.Joined != "" || r.Plan != "") {
			rs.Recent = append(rs.Recent, export(r))
		}
	}
	// weeks in a row with a round sat; this week doesn't break it until it's over
	w := weekStart(n)
	if !weeks[w] {
		w = w.AddDate(0, 0, -7)
	}
	for weeks[w] {
		rs.Weeks.Current++
		w = w.AddDate(0, 0, -7)
	}
	run := 0
	if len(weeks) > 0 {
		first := weekStart(n)
		for k := range weeks {
			if k.Before(first) {
				first = k
			}
		}
		for k := first; !k.After(weekStart(n)); k = k.AddDate(0, 0, 7) {
			if weeks[k] {
				run++
			} else if !k.Equal(weekStart(n)) {
				run = 0
			}
			rs.Weeks.Best = max(rs.Weeks.Best, run)
		}
	}
	rs.Weeks.Best = max(rs.Weeks.Best, rs.Weeks.Current)
	return rs
}

// attempted: the task was really worked on, so its topic is fair to show.
func attempted(t Task) bool {
	return t.Outcome == "solo" || t.Outcome == "assisted" || t.Outcome == "gaveup"
}

func countOutcome(t Task, tasks, solo, assisted, gaveup *int) {
	*tasks++
	switch t.Outcome {
	case "solo":
		*solo++
	case "assisted":
		*assisted++
	case "gaveup":
		*gaveup++
	}
}

// taskStats exports a task. Topic and tags only once it's been worked
// through: never for the current, a skipped or an ignored one.
func taskStats(t Task, finished bool) StatsTask {
	x := StatsTask{ID: t.ID, Source: t.Source(), Kind: t.Kind, Title: t.Title, URL: t.URL, Rating: t.Rating,
		Assigned: t.Assigned, Started: t.Started, Finished: t.Finished, Outcome: t.Outcome, Known: t.Known,
		WorkSec: t.WorkSec, DriftSec: t.DriftSec, Hints: t.Hints, Fails: t.Fails, Explain: t.Explain, Stage: t.ProbeStage}
	if finished {
		x.Credit = credit(t)
		if attempted(t) {
			x.Focus, x.Tags = t.Focus, t.Tags
		}
	}
	return x
}

// statsPath is where the daemon keeps stats.json: the runtime dir (tmpfs,
// no disk wear from a file that changes every tick during work).
func statsPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" && os.Getenv("EDGE_HOME") == "" {
		return filepath.Join(d, "edge", "stats.json")
	}
	return filepath.Join(dataDir(), "stats.json")
}

// statsBody is the document without its timestamp, to tell real changes
// from a tick.
func statsBody(st Stats) []byte {
	st.Generated = time.Time{}
	st.Daemon.Seen, st.Daemon.Gate = time.Time{}, time.Time{}
	b, _ := json.Marshal(st)
	return b
}

// statsWriter writes stats.json and pushes to Ember only when something changed.
type statsWriter struct {
	last     []byte
	lastPush time.Time
}

func (w *statsWriter) publish(st Stats) {
	body := statsBody(st)
	changed := !bytes.Equal(body, w.last)
	w.last = body
	if changed {
		if b, err := json.MarshalIndent(st, "", " "); err == nil {
			p := statsPath()
			if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
				writeAtomic(p, b, 0o644)
			}
		}
	}
	// resend now and then too: Ember forgets everything when it restarts
	if changed || time.Since(w.lastPush) > 5*time.Minute {
		if toEmber(map[string]any{"type": "stats", "stats": st}) {
			w.lastPush = time.Now()
		}
	}
}
