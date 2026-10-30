package main

import (
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Rounds are live contests: Codeforces, AtCoder and LeetCode. The habit is
// "sit it, or at least upsolve it", and edge runs the whole loop:
//
//	follow   the judges' calendars, keeping only rounds rated for the user
//	plan     hold them to [contests] per_week rounds a week, at most one a
//	         day, in study time; the rest stay open. "Can't make it" frees
//	         the day and another round takes the slot
//	brief    the day before; 15 minutes before, with last round's lesson
//	lock     while they're in a live round, Claude is off entirely: rounds
//	         ban AI, and the point is to find out what they can do alone
//	track    submissions as they land (Codeforces, LeetCode; AtCoder shows
//	         them only after the end)
//	settle   the round's credit, then the problems just past their last
//	         solve become upsolves, offered before any new problem. A round
//	         they were held to and missed owes its upsolves too
//	debrief  where the time went, one lesson, carried to the next round
//
// A round sat counts for the day: 2 credits, or one per problem solved.

type Round struct {
	ID       string         `json:"id"` // cf:2261 | ac:abc480 | lc:weekly-contest-524
	Name     string         `json:"name"`
	Kind     string         `json:"kind"` // div2 div3 div4 edu div12 | abc | weekly biweekly
	Start    time.Time      `json:"start"`
	DurSec   int            `json:"dur_sec"`
	Plan     string         `json:"plan,omitempty"` // in: held to it | out: they can't make it | "": open
	Joined   string         `json:"joined,omitempty"`
	JoinedAt time.Time      `json:"joined_at,omitzero"` // first submission: live | virtual
	Problems []RoundProblem `json:"problems,omitempty"`
	SubSeen  time.Time      `json:"sub_seen,omitzero"` // newest submission already counted
	Polled   time.Time      `json:"polled,omitzero"`
	Fails    int            `json:"poll_fails,omitempty"`
	Settled  time.Time      `json:"settled,omitzero"` // results in, upsolves queued
	Rated    bool           `json:"rated,omitempty"`  // the rating change is in
	Rating   int            `json:"rating,omitempty"`
	Delta    int            `json:"delta,omitempty"`
	Lesson   string         `json:"lesson,omitempty"`
	Gone     bool           `json:"gone,omitempty"` // dropped from the calendar before it began
}

type RoundProblem struct {
	ID     string    `json:"id"`    // the task id: cf:2261C | ac:abc480_d | lc:<slug>
	Index  string    `json:"index"` // C | D | Q3
	Title  string    `json:"title"`
	URL    string    `json:"url"`
	Rating int       `json:"rating,omitempty"` // Codeforces rating, AtCoder difficulty; 0 = not known yet
	State  string    `json:"state,omitempty"`  // "" | tried | solved | upsolve | upsolved | taught | dropped | lapsed
	At     int       `json:"at,omitempty"`     // seconds into the round it was solved
	Fails  int       `json:"fails,omitempty"`  // rejected submissions in the round
	Due    time.Time `json:"due,omitzero"`     // an upsolve lapses after this
}

func (r *Round) Judge() string  { return r.ID[:2] }
func (r *Round) Key() string    { return r.ID[3:] }
func (r *Round) End() time.Time { return r.Start.Add(time.Duration(r.DurSec) * time.Second) }
func (r *Round) Live(t time.Time) bool {
	return !r.Gone && !t.Before(r.Start) && t.Before(r.End())
}

// held: the user is held to it, or sat it anyway.
func (r *Round) held() bool { return r.Plan == "in" || r.Joined == "live" }

// Followed: a round rated for the user, from a judge they follow.
func followed(judge, id, name string, dur time.Duration) string {
	if !slices.Contains(cfg().RoundJudges, judge) {
		return ""
	}
	n := strings.ToLower(name)
	switch judge {
	case "cf":
		if dur > 5*time.Hour || containsAny(n, "kotlin", "unrated", "april fools", "testing round", "team") {
			return ""
		}
		switch {
		case strings.Contains(n, "educational"):
			return "edu"
		case containsAny(n, "div. 1 + div. 2", "div. 1 + 2", "div.1 + div.2", "div. 1+2", "global round", "codeton"):
			return "div12"
		case strings.Contains(n, "div. 2"):
			return "div2"
		case strings.Contains(n, "div. 3"):
			return "div3"
		case strings.Contains(n, "div. 4"):
			return "div4"
		}
	case "ac":
		if strings.HasPrefix(strings.TrimPrefix(id, "ac:"), "abc") {
			return "abc"
		}
	case "lc":
		switch slug := strings.TrimPrefix(id, "lc:"); {
		case strings.HasPrefix(slug, "weekly-contest"):
			return "weekly"
		case strings.HasPrefix(slug, "biweekly-contest"):
			return "biweekly"
		}
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(x string) bool { return strings.Contains(s, x) })
}

var (
	cfRoundNo = regexp.MustCompile(`(?:Educational Codeforces|Codeforces) Round (\d+)`)
	digits    = regexp.MustCompile(`\d+`)
)

// Short is the round's name for a pill: "CF 1128", "Edu 192", "ABC 480",
// "Weekly 524".
func (r *Round) Short() string {
	switch r.Kind {
	case "edu":
		if m := cfRoundNo.FindStringSubmatch(r.Name); m != nil {
			return "Edu " + m[1]
		}
		return "Edu round"
	case "abc":
		return "ABC " + digits.FindString(r.Key())
	case "weekly":
		return "Weekly " + digits.FindString(r.Key())
	case "biweekly":
		return "Biweekly " + digits.FindString(r.Key())
	}
	if m := cfRoundNo.FindStringSubmatch(r.Name); m != nil {
		return "CF " + m[1]
	}
	return "CF " + kindLabel(r.Kind)
}

func kindLabel(kind string) string {
	return map[string]string{"div2": "Div. 2", "div3": "Div. 3", "div4": "Div. 4", "edu": "Educational",
		"div12": "Div. 1 + 2", "abc": "Beginner", "weekly": "Weekly", "biweekly": "Biweekly"}[kind]
}

// RoundURL is the round's page; regURL is where to register for it.
func (r *Round) RoundURL() string {
	switch r.Judge() {
	case "cf":
		return "https://codeforces.com/contest/" + r.Key()
	case "ac":
		return "https://atcoder.jp/contests/" + r.Key()
	}
	return "https://leetcode.com/contest/" + r.Key() + "/"
}

func (r *Round) regURL() string {
	if r.Judge() == "cf" {
		return "https://codeforces.com/contestRegistration/" + r.Key()
	}
	return r.RoundURL()
}

// judgeDir is where competitest writes a judge's files in the workspace.
func judgeDir(judge string) string {
	return map[string]string{"cf": "codeforces", "ac": "atcoder", "lc": "leetcode"}[judge]
}

func (s *State) round(id string) *Round {
	for i := range s.Rounds {
		if s.Rounds[i].ID == id {
			return &s.Rounds[i]
		}
	}
	return nil
}

// ---- following the calendars ----

const (
	planAhead  = 8 * 24 * time.Hour // rounds this far out are planned
	planNotice = 6 * time.Hour      // and never less than this before they start
	keepRounds = 120 * 24 * time.Hour
)

// applyCalendar merges a judge's calendar: new rounds are added, moved ones
// follow, and an announced one that vanished before starting is gone.
func (s *State) applyCalendar(judge string, cal []calRound) {
	n := now()
	seen := map[string]bool{}
	var horizon time.Time // the calendar only vouches for rounds up to its last
	for _, c := range cal {
		if c.Start.After(horizon) {
			horizon = c.Start
		}
		kind := followed(judge, c.ID, c.Name, c.Dur)
		if kind == "" {
			continue
		}
		seen[c.ID] = true
		if r := s.round(c.ID); r != nil {
			if r.Settled.IsZero() {
				r.Name, r.Kind, r.Start, r.DurSec, r.Gone = c.Name, kind, c.Start, int(c.Dur.Seconds()), false
			}
			continue
		}
		if c.Start.Add(c.Dur).Before(n) {
			continue
		}
		s.Rounds = append(s.Rounds, Round{ID: c.ID, Name: c.Name, Kind: kind, Start: c.Start, DurSec: int(c.Dur.Seconds())})
	}
	for i := range s.Rounds {
		r := &s.Rounds[i]
		if r.Judge() == judge && !seen[r.ID] && !r.Gone && r.Start.After(n.Add(time.Hour)) && !r.Start.After(horizon) {
			log.Println("round gone from the calendar:", r.ID)
			r.Gone = true
		}
	}
	slices.SortStableFunc(s.Rounds, func(a, b Round) int { return a.Start.Compare(b.Start) })
}

// suits: the round sits inside study time and ends before the day does.
func suits(r *Round) bool {
	return studyTime(r.Start) && !r.End().After(day(r.Start).AddDate(0, 0, 1).Add(cfg().DayEnds))
}

// planRounds holds the user to per_week rounds in each week: one a day at
// most, spread across judges, earliest first. What's decided stays decided;
// a round they can't make frees its day and the slot goes to another.
func (s *State) planRounds() {
	c := cfg()
	if !c.Rounds {
		return
	}
	n := now()
	weeks := map[time.Time][]*Round{}
	for i := range s.Rounds {
		if r := &s.Rounds[i]; !r.Gone {
			weeks[weekStart(r.Start)] = append(weeks[weekStart(r.Start)], r)
		}
	}
	for _, rs := range weeks {
		in, days, judges := 0, map[time.Time]bool{}, map[string]int{}
		for _, r := range rs {
			switch r.Plan {
			case "in":
				in++
				judges[r.Judge()]++
				fallthrough
			case "out":
				days[day(r.Start)] = true
			}
		}
		rank := func(r *Round) [3]int64 {
			return [3]int64{int64(judges[r.Judge()]), int64(slices.Index(c.RoundJudges, r.Judge())), r.Start.Unix()}
		}
		for in < c.PerWeek {
			var best *Round
			for _, r := range rs {
				lead := r.Start.Sub(n)
				if r.Plan != "" || r.Joined != "" || lead < planNotice || lead > planAhead || !suits(r) || days[day(r.Start)] ||
					!slices.Contains(c.RoundJudges, r.Judge()) {
					continue
				}
				if best == nil {
					best = r
				} else if a, b := rank(r), rank(best); slices.Compare(a[:], b[:]) < 0 {
					best = r
				}
			}
			if best == nil {
				break
			}
			best.Plan = "in"
			log.Println("round planned:", best.ID, best.Name)
			in++
			days[day(best.Start)] = true
			judges[best.Judge()]++
		}
	}
}

// ---- the live round and the lock ----

// liveHeld is the live round they're held to, or joined.
func (s *State) liveHeld() *Round {
	if !cfg().Rounds {
		return nil
	}
	return s.heldNow()
}

func (s *State) heldNow() *Round {
	n := now()
	for i := range s.Rounds {
		if r := &s.Rounds[i]; r.Live(n) && r.held() {
			return r
		}
	}
	return nil
}

// roundLock is the live round that keeps Claude off. Read by the hook, so
// no IO.
func (s *State) roundLock() *Round {
	if !s.rules().RoundLock {
		return nil // pinned for the day: switching it off mid-round waits for tomorrow
	}
	return s.heldNow()
}

func lockReason(r *Round) string {
	return fmt.Sprintf("edge: you're in %s until %s. No AI during a round: the judges ban it, and the point is what you can do alone. "+
		"Claude opens again when it ends. Can't sit it after all? The island's \"Can't make it\" (or `edge action skipround:%s` in your own terminal) lets you out, and its problems become upsolves.",
		r.Name, r.End().Format("15:04"), r.ID)
}

// roundObs is one look at a round: its problems and the user's submissions.
type roundObs struct {
	ID       string
	Err      error
	Problems []RoundProblem // nil: not fetched
	Subs     []roundSub
	Final    bool // taken after the round ended: settle on it
}

// settleAfter is how long after the end a round's results are taken as final.
func settleAfter(judge string) time.Duration {
	if judge == "ac" {
		return 15 * time.Minute // kenkoooo picks AtCoder's submissions up within minutes
	}
	return 10 * time.Minute
}

// observeDue: which rounds this tick should look at. Live ones every minute
// on judges that show submissions live; finished ones until they settle.
func (s *State) observeDue() []Round {
	if !cfg().Rounds {
		return nil
	}
	n := now()
	var out []Round
	for _, r := range s.Rounds {
		if r.Gone || !r.Settled.IsZero() || n.Before(r.Start) {
			continue
		}
		wait := pollEvery
		if r.Fails > 0 {
			wait = backoff(r.Fails)
		}
		if n.Sub(r.Polled) < wait {
			continue
		}
		switch {
		case r.Live(n) && r.Judge() != "ac":
			out = append(out, r)
		case !n.Before(r.End().Add(settleAfter(r.Judge()))):
			if n.Sub(r.Polled) >= 5*time.Minute || r.Polled.Before(r.End()) {
				out = append(out, r)
			}
		}
	}
	return out
}

// observe fetches one round. The problem list is fetched once: at the start
// for a round they're held to, otherwise once they've joined or it's owed.
func observe(r Round, n time.Time) roundObs {
	o := roundObs{ID: r.ID, Final: !n.Before(r.End().Add(settleAfter(r.Judge())))}
	needList := len(r.Problems) == 0 && (r.Plan != "" || r.Joined != "" || r.Judge() == "lc")
	if needList && r.Judge() == "lc" {
		// LeetCode submissions are matched to the round through its problems
		if o.Problems, o.Err = roundProblems(&r); o.Err != nil {
			return o
		}
		r.Problems, needList = o.Problems, false
	}
	if o.Subs, o.Err = roundSubs(&r); o.Err != nil {
		return o
	}
	joined := r.Joined != "" || slices.ContainsFunc(o.Subs, func(x roundSub) bool { return x.Type == "live" })
	if len(r.Problems) == 0 && (needList || joined) {
		o.Problems, o.Err = roundProblems(&r)
	}
	return o
}

// applyObs takes what a look at a round found.
func (s *State) applyObs(o roundObs) {
	r := s.round(o.ID)
	if r == nil || !r.Settled.IsZero() {
		return
	}
	r.Polled = now()
	if o.Err != nil {
		r.Fails++
		log.Printf("round %s: %v", r.ID, o.Err)
		if o.Final && now().Sub(r.End()) > 3*24*time.Hour {
			s.settle(r) // the judge never told: settle on what's known
		}
		return
	}
	r.Fails = 0
	s.mergeProblems(r, o.Problems)
	s.count(r, o.Subs, "live")
	if o.Final {
		s.settle(r)
	}
}

// mergeProblems takes a fresh problem list, keeping what's known about each.
func (s *State) mergeProblems(r *Round, ps []RoundProblem) {
	if ps == nil {
		return
	}
	old := r.Problems
	r.Problems = slices.Clone(ps)
	for i := range r.Problems {
		for _, o := range old {
			if o.ID == r.Problems[i].ID {
				p := &r.Problems[i]
				p.State, p.At, p.Fails, p.Due = o.State, o.At, o.Fails, o.Due
				p.Rating = max(p.Rating, o.Rating)
			}
		}
	}
	for _, o := range old { // touched before the list came: keep it
		if !slices.ContainsFunc(r.Problems, func(p RoundProblem) bool { return p.ID == o.ID }) {
			r.Problems = append(r.Problems, o)
		}
	}
}

// count applies submissions of one type (live or virtual) to the round's
// problems, each once.
func (s *State) count(r *Round, subs []roundSub, typ string) {
	for _, x := range subs {
		if x.Type != typ || !x.At.After(r.SubSeen) {
			continue
		}
		if x.Pending {
			break // judged on a later look
		}
		r.SubSeen = x.At
		if r.Joined == "" {
			r.Joined, r.JoinedAt = typ, x.At
			log.Printf("joined %s (%s)", r.ID, typ)
		}
		i := slices.IndexFunc(r.Problems, func(p RoundProblem) bool { return p.ID == x.Problem })
		if i < 0 {
			r.Problems = append(r.Problems, RoundProblem{ID: x.Problem, Index: indexOf(x.Problem)})
			i = len(r.Problems) - 1
		}
		p := &r.Problems[i]
		if p.State == "solved" {
			continue
		}
		if x.ok() {
			p.State = "solved"
			if typ == "live" {
				p.At = int(x.At.Sub(r.Start).Seconds())
			}
		} else if x.Verdict != "COMPILATION_ERROR" && x.Verdict != "CE" && x.Verdict != "Compile Error" {
			p.State = "tried"
			p.Fails++
		}
	}
}

// indexOf guesses a problem's letter from its id, before the list is known.
func indexOf(id string) string {
	switch id[:2] {
	case "cf":
		return strings.TrimPrefix(strings.TrimPrefix(id, "cf:"), contestOf(id))
	case "ac":
		if i := strings.LastIndexByte(id, '_'); i >= 0 {
			return strings.ToUpper(id[i+1:])
		}
	}
	return "?"
}

func (r *Round) solved() int {
	n := 0
	for _, p := range r.Problems {
		if p.State == "solved" {
			n++
		}
	}
	return n
}

// missedFrom is where a round's owed upsolves start when they didn't sit
// it: as far as they got in their last round of the same kind, or a fair
// guess for their level.
func (s *State) missedFrom(r *Round) int {
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		o := &s.Rounds[i]
		if o.ID != r.ID && o.Kind == r.Kind && o.Joined != "" && !o.Settled.IsZero() && r.Start.Sub(o.Start) < keepRounds {
			return o.solved()
		}
	}
	return map[string]int{"div2": 2, "div12": 1, "edu": 2, "div3": 3, "div4": 4, "abc": 3, "weekly": 2, "biweekly": 2}[r.Kind]
}

// settle closes a round: credit is in (it comes from Joined), and the
// problems just past the last solve become upsolves, one after another.
// A round they were held to, or said they couldn't make, owes them too.
func (s *State) settle(r *Round) {
	r.Settled = now()
	from := 0
	switch {
	case r.Joined != "":
	case r.Plan != "":
		from = s.missedFrom(r)
	default:
		return // an open round they didn't sit owes nothing
	}
	s.queueUpsolves(r, from)
	if t := s.Current; t != nil && !t.Active() && (t.Kind == "new" || t.Kind == "resolve") {
		putBack(s, now()) // the upsolve goes first; this one comes back after it
	}
}

func (s *State) queueUpsolves(r *Round, from int) {
	due := day(now()).AddDate(0, 0, cfg().UpsolveDays+1).Add(cfg().DayEnds)
	left := cfg().UpsolveN
	for i := range r.Problems {
		p := &r.Problems[i]
		if left == 0 {
			break
		}
		if i < from || p.State == "solved" || p.State == "upsolved" || p.State == "taught" {
			continue
		}
		if p.State != "upsolve" {
			p.State, p.Due = "upsolve", due
		}
		left--
	}
}

// nextUpsolve is the upsolve to offer: the oldest round's first open one.
// A round's second waits for its first.
func (s *State) nextUpsolve() (*Round, *RoundProblem) {
	n := now()
	for i := range s.Rounds {
		r := &s.Rounds[i]
		for j := range r.Problems {
			p := &r.Problems[j]
			if p.State == "upsolve" {
				if p.Due.After(n) {
					return r, p
				}
				continue // lapsed; tidyRounds marks it
			}
		}
	}
	return nil, nil
}

// upsolveDone records how an upsolve task ended on its round.
func (s *State) upsolveDone(t *Task) {
	r := s.round(t.Round)
	if r == nil {
		return
	}
	for i := range r.Problems {
		if p := &r.Problems[i]; p.ID == t.ID {
			switch t.Outcome {
			case "solo", "assisted":
				p.State = "upsolved"
			case "gaveup":
				p.State = "taught"
			case "skipped":
				p.State = "dropped"
			}
		}
	}
}

// tidyRounds lapses upsolves past their day, unless one is being worked on
// right now, and forgets old rounds. A current upsolve that lapses before it
// was started stops being current: due passes, it's lapsed, it isn't
// startable, and the next tick hands out what's next.
func (s *State) tidyRounds() {
	n := now()
	for i := range s.Rounds {
		r := &s.Rounds[i]
		for j := range r.Problems {
			p := &r.Problems[j]
			cur := s.Current != nil && s.Current.ID == p.ID && s.Current.Round == r.ID
			if p.State == "upsolve" && !p.Due.After(n) && !(cur && s.Current.Active()) {
				p.State = "lapsed"
				if cur {
					log.Println("lapsed before it was started:", p.ID)
					putBack(s, n)
				}
			}
			if cur && s.Current != nil && s.Current.Rating == 0 && p.Rating > 0 {
				s.Current.Rating = p.Rating // CF rates a round's problems after it's assigned
			}
		}
	}
	s.Rounds = slices.DeleteFunc(s.Rounds, func(r Round) bool {
		return n.Sub(r.End()) > keepRounds || r.Gone && n.Sub(r.Start) > 24*time.Hour
	})
}

// virtuals: a Codeforces round they missed and then sat virtually counts as
// sat, on the day they did it. Fed from the regular sync's submissions.
func (s *State) virtuals(subs []cfSub) {
	n := now()
	for i := range s.Rounds {
		r := &s.Rounds[i]
		if r.Judge() != "cf" || r.Joined != "" || r.Settled.IsZero() || n.Sub(r.End()) > time.Duration(cfg().UpsolveDays)*24*time.Hour {
			continue
		}
		var vs []roundSub
		for _, x := range subs {
			if x.Author.Type == "VIRTUAL" && fmt.Sprint(x.Problem.ContestID) == r.Key() {
				vs = append(vs, roundSub{Problem: x.Problem.key(), At: time.Unix(x.Time, 0), Verdict: x.Verdict, Type: "virtual"})
			}
		}
		if len(vs) == 0 {
			continue
		}
		slices.SortFunc(vs, func(a, b roundSub) int { return a.At.Compare(b.At) })
		s.count(r, vs, "virtual")
		for j := range r.Problems { // re-aim the owed upsolves at where the virtual stopped
			if p := &r.Problems[j]; p.State == "upsolve" && !(s.Current != nil && s.Current.ID == p.ID) {
				p.State = ""
			}
		}
		s.queueUpsolves(r, 0)
	}
}

// rateRounds takes rating changes from the judges' histories.
func (s *State) rateRounds() {
	for i := range s.Rounds {
		r := &s.Rounds[i]
		if r.Rated || r.Joined != "live" {
			continue
		}
		hist := s.RatingLog[r.Judge()]
		for j, pt := range hist {
			if pt.Round == r.ID {
				r.Rated, r.Rating, r.Delta = true, pt.Rating, pt.Rating
				if j > 0 {
					r.Delta = pt.Rating - hist[j-1].Rating
				}
			}
		}
	}
}

// awaitingRating: a round they sat ended in the last 3 days and its rating
// change isn't in yet, so that judge is synced every half hour.
func (s *State) awaitingRating(judge string) bool {
	n := now()
	return slices.ContainsFunc(s.Rounds, func(r Round) bool {
		return r.Judge() == judge && r.Joined == "live" && !r.Rated && n.After(r.End()) && n.Sub(r.End()) < 3*24*time.Hour
	})
}

// ---- credit ----

// roundCredit: sitting a round meets its day, and each problem solved in it
// counts one beyond that.
func (s *State) roundCredit(r *Round) int {
	if r.Joined == "" {
		return 0
	}
	return max(2, r.solved(), s.dailyMin(r.creditAt()))
}

// creditAt is when a round's credit lands: its start, or the virtual's.
func (r *Round) creditAt() time.Time {
	if r.Joined == "virtual" {
		return r.JoinedAt
	}
	return r.Start
}

// ---- what to say ----

func clockDay(t time.Time) string {
	n := now()
	switch d := int(day(t).Sub(day(n)).Hours() / 24); {
	case d == 0 && t.Hour() >= 17:
		return "Tonight " + t.Format("15:04")
	case d == 0:
		return "Today " + t.Format("15:04")
	case d == 1:
		return "Tomorrow " + t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

func hours(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return dur(d)
}

// lastLesson is the lesson from their most recent debrief.
func (s *State) lastLesson() string {
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if l := s.Rounds[i].Lesson; l != "" {
			return l
		}
	}
	return ""
}

// letters: "A B C" for the problems in a state.
func (r *Round) letters(states ...string) string {
	var ls []string
	for _, p := range r.Problems {
		if slices.Contains(states, p.State) {
			ls = append(ls, p.Index)
		}
	}
	return strings.Join(ls, " ")
}

func (r *Round) clock(t time.Time) string {
	d := max(0, r.End().Sub(t)).Round(time.Minute)
	return fmt.Sprintf("%d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// roundEvents is what rounds have to say right now. lock is the live round
// they're in, if any.
func (s *State) roundEvents(w window, dt time.Duration) []event {
	c := cfg()
	if !c.Rounds {
		return nil
	}
	n := now()
	var evs []event
	lesson := ""
	if l := s.lastLesson(); l != "" {
		lesson = "\nLast lesson: " + l
	}
	for i := range s.Rounds {
		r := &s.Rounds[i]
		if r.Gone {
			continue
		}
		lead := r.Start.Sub(n)
		if r.Plan == "in" && r.Joined == "" {
			if lead <= 30*time.Hour && lead > time.Hour && n.Hour() >= 9 && n.Hour() < 23 {
				evs = append(evs, event{Key: "roundeve:" + r.ID, Title: clockDay(r.Start) + " · " + r.Name,
					Body: fmt.Sprintf("%s · %s · you're in. Sitting it counts for the day; Claude is off while it runs.%s",
						kindLabel(r.Kind), hours(r.End().Sub(r.Start)), lesson),
					Actions: []action{{"skipround:" + r.ID, "Can't make it"}, {"register:" + r.ID, "Register"}}})
			}
			if lead <= 15*time.Minute && lead > 0 {
				evs = append(evs, event{Key: "roundsoon:" + r.ID, Urgent: true, Title: fmt.Sprintf("%s starts in %d min", r.Short(), int(lead.Round(time.Minute).Minutes())),
					Body:    fmt.Sprintf("Registered? No AI from %s to %s.%s", r.Start.Format("15:04"), r.End().Format("15:04"), lesson),
					Actions: []action{{"skipround:" + r.ID, "Can't make it"}, {"openround:" + r.ID, "Open the round"}}})
			}
			if r.Live(n) && r.Judge() != "ac" && n.Sub(r.Start) >= 10*time.Minute && !working(w, &Task{}, false, s.practiceDir()) {
				evs = append(evs, event{Key: "roundlate:" + r.ID, Urgent: true, Title: fmt.Sprintf("%s started %s ago", r.Short(), dur(n.Sub(r.Start))),
					Body:    fmt.Sprintf("%s left. Late still counts: every problem solved does.", r.clock(n)),
					Actions: []action{{"skipround:" + r.ID, "Skip it"}, {"openround:" + r.ID, "Open the round"}}})
			}
		}
		if r.Live(n) && r.held() {
			if re := c.Distraction; re != nil && re.MatchString(w.Title) {
				if s.DriftAt.IsZero() {
					s.DriftAt = n
				} else if n.Sub(s.DriftAt) >= time.Minute {
					evs = append(evs, event{Key: "rounddrift:" + r.ID, Every: 5 * time.Minute, Urgent: true,
						Title: fmt.Sprintf("You're in %s · %s left", r.Short(), r.clock(n)), Body: "You're on " + re.FindString(w.Title) + "."})
				}
			} else {
				s.DriftAt = time.Time{}
			}
		}
		if !r.Settled.IsZero() && n.Sub(r.Settled) < 12*time.Hour {
			up := r.letters("upsolve")
			switch {
			case r.Joined != "":
				title := r.Short() + " · nothing solved"
				if k := r.letters("solved"); k != "" {
					title = r.Short() + " · solved " + k
				}
				if r.Joined == "virtual" {
					title += " (virtual)"
				}
				body := "Counts for the day."
				if up != "" {
					body += " Upsolve next: " + up + "."
				}
				acts := []action{{"debrief", "Debrief (3 min)"}}
				if up != "" {
					acts = []action{{"upsolve", "Upsolve now"}, {"debrief", "Debrief (3 min)"}}
				}
				evs = append(evs, event{Key: "roundover:" + r.ID + ":" + r.Joined, Title: title, Body: body, Actions: acts})
			case r.Plan != "" && up != "":
				body := fmt.Sprintf("You still owe it: %s, before %s.", up, r.Problems[slices.IndexFunc(r.Problems, func(p RoundProblem) bool { return p.State == "upsolve" })].Due.Add(-time.Minute).Format("Mon"))
				if r.Judge() == "cf" {
					body += " Sitting it virtually counts as sitting it."
				}
				evs = append(evs, event{Key: "roundmissed:" + r.ID, Title: "You missed " + r.Short(), Body: body,
					Actions: []action{{"upsolve", "Upsolve now"}}})
			}
		}
		if r.Rated && n.Sub(r.End()) < 4*24*time.Hour {
			sign := "+"
			if r.Delta < 0 {
				sign = "−"
			}
			body := "Rating's in."
			if up := r.letters("upsolve"); up != "" {
				body += " Still to upsolve: " + up + "."
			}
			evs = append(evs, event{Key: "rated:" + r.ID, Title: fmt.Sprintf("%s: %s%d → %d", r.Short(), sign, abs(r.Delta), r.Rating), Body: body})
		}
	}
	return evs
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// roundToday is a round they're held to later today: the day's work is
// planned, so the gate waits for it. Miss it and the day's tasks are owed.
func (s *State) roundToday() *Round {
	n := now()
	for i := range s.Rounds {
		if r := &s.Rounds[i]; !r.Gone && r.Plan == "in" && r.Joined == "" && day(r.Start).Equal(day(n)) && n.Before(r.Start) {
			return r
		}
	}
	return nil
}

// nextHeld is the next round they're held to that hasn't ended.
func (s *State) nextHeld() *Round {
	n := now()
	for i := range s.Rounds {
		if r := &s.Rounds[i]; !r.Gone && r.Plan == "in" && n.Before(r.End()) {
			return r
		}
	}
	return nil
}

// ---- debrief ----

// roundInfo prints a round for the debrief skill: what happened, minute by
// minute, and what's owed.
func roundInfo(s *State) error {
	r := s.debriefRound()
	if r == nil {
		return fmt.Errorf("no round to debrief yet")
	}
	fmt.Fprintf(out, "ROUND %s (%s) · %s · %s\n", r.Name, r.RoundURL(), r.Start.Format("Mon 2 Jan 15:04"), hours(r.End().Sub(r.Start)))
	switch r.Joined {
	case "live":
		fmt.Fprintf(out, "sat it live · solved %d · credit %d\n", r.solved(), s.roundCredit(r))
	case "virtual":
		fmt.Fprintf(out, "sat it virtually on %s · solved %d\n", r.JoinedAt.Format("Mon 2 Jan"), r.solved())
	default:
		fmt.Fprintln(out, "didn't sit it")
	}
	if r.Rated {
		fmt.Fprintf(out, "rating %+d → %d\n", r.Delta, r.Rating)
	}
	for _, p := range r.Problems {
		line := fmt.Sprintf("  %-3s %s", p.Index, p.Title)
		if p.Rating > 0 {
			line += fmt.Sprintf(" (%d)", p.Rating)
		}
		switch p.State {
		case "solved":
			if p.At > 0 {
				line += fmt.Sprintf(" · solved at %s", dur(time.Duration(p.At)*time.Second))
			} else {
				line += " · solved"
			}
		case "tried":
			line += " · not solved"
		case "upsolve":
			line += " · UPSOLVE, due " + p.Due.Add(-time.Minute).Format("Mon")
		case "":
			line += " · not opened"
		default:
			line += " · " + p.State
		}
		if p.Fails > 0 {
			line += fmt.Sprintf(" · %d rejected", p.Fails)
		}
		fmt.Fprintln(out, line)
	}
	if r.Lesson != "" {
		fmt.Fprintln(out, "lesson recorded:", r.Lesson)
	} else {
		fmt.Fprintln(out, "Find where the time went and one concrete lesson, then `edge round lesson <one line, plain words>`.")
	}
	return nil
}

// debriefRound is the latest finished round they sat.
func (s *State) debriefRound() *Round {
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if r := &s.Rounds[i]; r.Joined != "" && !r.Settled.IsZero() {
			return r
		}
	}
	return nil
}

func setLesson(s *State, text string) error {
	r := s.debriefRound()
	if r == nil {
		return fmt.Errorf("no round to debrief yet")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("the lesson is empty")
	}
	r.Lesson = text
	fmt.Fprintf(out, "lesson kept for %s; it comes back before your next round.\n", r.Short())
	return nil
}

// roundsReport is `edge rounds`: the plan and the record.
func roundsReport(s *State) string {
	var b strings.Builder
	n := now()
	if !cfg().Rounds {
		return "rounds are off ([contests] enabled = false)\n"
	}
	fmt.Fprintf(&b, "rounds: %d a week, judges %s, lock %v\n", cfg().PerWeek, strings.Join(cfg().RoundJudges, " "), s.rules().RoundLock)
	for _, r := range s.Rounds {
		if r.Gone || !n.Before(r.End()) || r.Start.Sub(n) > planAhead {
			continue
		}
		plan := map[string]string{"in": "YOU'RE IN", "out": "can't make it", "": "open"}[r.Plan]
		if r.Live(n) {
			plan += " · LIVE, " + r.clock(n) + " left"
		}
		fmt.Fprintf(&b, "  %s  %-10s %s · %s\n", r.Start.Format("Mon 02 Jan 15:04"), r.Short(), r.Name, plan)
	}
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		r := &s.Rounds[i]
		if r.Settled.IsZero() || (r.Joined == "" && r.Plan == "") {
			continue
		}
		res := "missed"
		if r.Joined != "" {
			res = "solved " + orNone(r.letters("solved"))
		}
		if up := r.letters("upsolve"); up != "" {
			res += " · upsolve " + up
		}
		if done := r.letters("upsolved", "taught"); done != "" {
			res += " · upsolved " + done
		}
		fmt.Fprintf(&b, "  %s  %-10s %s\n", r.Start.Format("Mon 02 Jan"), r.Short(), res)
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
