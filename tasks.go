package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"time"
)

// knownUnder: a "new" problem solved this fast was almost certainly seen
// before (LeetCode only shows the last 20 accepts, so edge can't know).
const knownUnder = 5 * time.Minute

// maxTries: a re-solve failed this many times at one stage is dropped. It
// was taught each time; a fourth cold attempt teaches nothing new.
const maxTries = 3

// assign picks the next task and makes it current, not yet started: the
// clock starts when the user begins (Start button, the file appearing, or
// a first submission), so hint timers measure real work.
func assign(s *State, cat *catalog) (*Task, error) {
	t := &Task{}
	switch {
	case s.gapDays() >= 2 && cat.probs("cf") != nil:
		if p := s.pickReentry(cat.probs("cf")); p != nil {
			*t = Task{ID: p.ID, Kind: "reentry", Title: p.Title, URL: p.URL, Rating: p.Rating, Tags: p.Tags, Policy: "cold"}
			break
		}
		fallthrough
	default:
		// an upsolve while the round is fresh, before re-solves and new problems
		if r, p := s.nextUpsolve(); p != nil {
			*t = Task{ID: p.ID, Kind: "upsolve", Title: p.Title, URL: p.URL, Rating: p.Rating, Policy: "ladder",
				Round: r.ID, RoundName: r.Short() + " " + p.Index}
			break
		}
		if i := s.dueProbe(); i >= 0 {
			p := s.Probes[i]
			*t = Task{ID: p.Problem, Kind: "resolve", Title: p.Title, URL: p.URL, Rating: p.Rating,
				Policy: "cold", Arm: p.Policy, ProbeOf: p.Problem, ProbeStage: p.Stage, ProbeTry: p.Try}
			s.Probes = slices.Delete(s.Probes, i, i+1)
			break
		}
		if err := s.newTask(t, cat); err != nil {
			s.trouble("assign", "can't pick a problem: "+err.Error())
			return nil, err
		}
	}
	s.fixed("assign")
	t.Assigned, t.Explain = now(), -1
	s.Current = t
	if t.Kind != "upsolve" {
		archive(t) // the old attempt goes before they can open it; an upsolve picks up the round's
	}
	return t, nil
}

func (s *State) newTask(t *Task, cat *catalog) error {
	src := s.nextSource()
	var errs []string
	for _, src := range []string{src, other(src)} {
		if !slices.Contains(cfg().Sources, src) {
			continue
		}
		probs := cat.probs(src)
		if probs == nil {
			errs = append(errs, fmt.Sprintf("%s unavailable: %v", srcName(src), cat.err[src]))
			continue
		}
		solved, struggled := s.focusCounts(src, probs)
		focus := cfg().Focus[src]
		for try := 0; try < 20; try++ {
			f := ""
			if len(focus) > 0 && rand.Float64() >= cfg().AnyTopic {
				f = pickFocus(focus, solved, struggled)
			}
			if try == 19 {
				f = "" // last resort: anything in the band
			}
			if p := s.pickNew(src, probs, f); p != nil {
				*t = Task{ID: p.ID, Kind: "new", Title: p.Title, URL: p.URL, Rating: p.Rating, Tags: p.Tags, Focus: f}
				return nil
			}
		}
		lo, hi := s.window(src)
		errs = append(errs, fmt.Sprintf("no unseen %s problem in the %d-%d band", srcName(src), lo, hi))
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

func other(src string) string {
	if src == "cf" {
		return "lc"
	}
	return "cf"
}

// begin starts the clock. A new problem gets its hint policy now.
func begin(s *State, t *Task, at time.Time) {
	if t.Active() {
		return
	}
	t.Started = at
	if t.Kind == "new" && t.Policy == "" {
		t.Policy = s.pickPolicy()
		t.Arm = t.Policy
	}
}

func describe(s *State, t *Task) string {
	var b strings.Builder
	label := map[string]string{"new": "NEW", "resolve": "COLD RE-SOLVE", "reentry": "WARM-UP (welcome back)", "upsolve": "UPSOLVE " + t.RoundName}[t.Kind]
	fmt.Fprintf(&b, "%s  %s\n  %s\n  rating %d", label, t.Title, t.URL, t.Rating)
	switch t.Kind {
	case "new":
		b.WriteString(" · topic revealed after you solve it\n")
	case "upsolve":
		b.WriteString(" · from the round; hints unlock as you work\n")
	default:
		b.WriteString(" · no hints, from scratch, no peeking at your old code\n")
	}
	if t.Active() {
		fmt.Fprintf(&b, "  started %s ago · %s of work · %d rejected\n", since(t.Started), dur(t.Worked()), t.Fails)
	} else {
		b.WriteString("  not started yet\n")
	}
	got, need := s.today()
	fmt.Fprintf(&b, "today %d/%d · week %d/%d\n", got, need, s.creditsSince(weekStart(now())), cfg().Weekly)
	return b.String()
}

// stuck shows the hint state. take > 0 claims hint level take: idempotent,
// so re-running it never spends a second hint.
func stuck(s *State, take int, statement string) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	begin(s, t, now())
	fmt.Fprintf(out, "problem: %s %s (rating %d, tags hidden) · %s of work, %d rejected submissions\n",
		t.Title, t.URL, t.Rating, dur(t.Worked()), t.Fails)
	if statement != "" {
		fmt.Fprintf(out, "statement:\n%s\n", statement)
	}
	if p := solutionPath(t); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Fprintf(out, "their current code (%s):\n%s\n", p, b)
		}
	}
	lvl, wait := hintAllowance(t)
	switch {
	case take > 0 && take <= t.Hints:
		fmt.Fprintf(out, "HINT LEVEL %d granted (%d of 3 used)\n", take, t.Hints)
		return nil
	case take > 0 && take == t.Hints+1 && take <= lvl:
		t.Hints = take
		fmt.Fprintf(out, "HINT LEVEL %d granted (%d of 3 used)\n", take, t.Hints)
		return nil
	case take > 0:
		return fmt.Errorf("hint level %d isn't available: %d used, up to %d allowed now", take, t.Hints, lvl)
	}
	switch {
	case lvl > t.Hints:
		fmt.Fprintf(out, "HINT LEVEL %d available (%d of 3 used). Claim it with `edge stuck --take %d`.\n", t.Hints+1, t.Hints, t.Hints+1)
	case t.Policy == "cold":
		fmt.Fprintln(out, "HINTS: none. This is a cold re-solve. If they can't do it, `edge giveup` reschedules it.")
	case t.Hints >= 3:
		fmt.Fprintln(out, "HINTS: all 3 used. Next step is `edge giveup` (full solution) when allowed.")
	default:
		fmt.Fprintf(out, "HINTS: locked. The next hint unlocks after %s more work.\n", dur(wait))
	}
	return nil
}

// verifyAC asks the judge for an accepted submission of t. Network: call it
// outside the lock.
func verifyAC(t *Task) (bool, error) {
	from := t.Assigned.Add(-time.Minute).Unix()
	if t.Source() == "cf" {
		subs, err := cfSubs(100)
		if err != nil {
			return false, err
		}
		for _, x := range subs {
			if x.Problem.key() == t.ID && x.Verdict == "OK" && x.Time >= from {
				return true, nil
			}
		}
		return false, nil
	}
	if t.Source() == "ac" {
		subs, err := acSubs(t.Assigned.Add(-time.Minute))
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(subs, func(x acSub) bool { return "ac:"+x.Problem == t.ID && x.Result == "AC" }), nil
	}
	subs, err := lcSubs()
	if err != nil {
		return false, err
	}
	for _, x := range subs {
		if "lc:"+x.Slug == t.ID && x.Status == "Accepted" && x.unix() >= from {
			return true, nil
		}
	}
	return false, nil
}

// accept records an accepted solution.
func accept(s *State, t *Task, at time.Time) {
	begin(s, t, at)
	t.Outcome = "solo"
	if t.Hints > 0 {
		t.Outcome = "assisted"
	}
	if t.Kind == "new" && t.Hints == 0 && at.Sub(t.Started) < knownUnder && t.Worked() < knownUnder {
		t.Known = true
		s.Seen[t.ID] = true
	}
	finish(s, t, at)
}

// done records the current task's AC and starts its explain-back. The
// daemon usually records the AC first and may already have handed out the
// next task; then the explain-back still waiting is what done is for.
func done(s *State, ok bool, verifyErr error) error {
	t := s.Current
	if t == nil {
		return explainInfo(s)
	}
	if !ok {
		if p := s.lastSolved(); p != nil {
			fmt.Fprintf(out, "(%s isn't accepted yet; this is the explain-back for %s)\n", t.Title, p.Title)
			return explainInfo(s)
		}
		if verifyErr != nil {
			return verifyErr
		}
		return fmt.Errorf("no accepted submission for %s yet; submit it first", t.Title)
	}
	accept(s, t, now())
	fmt.Fprintf(out, "ACCEPTED (%s) in %s\n", t.Outcome, since(t.Started))
	if t.Known {
		fmt.Fprintln(out, "That was fast enough that they'd likely seen it before: it won't move the band or get re-solves.")
	}
	return explainInfo(s)
}

func giveup(s *State, statement string) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	begin(s, t, now())
	if ok, wait := canGiveUp(t); !ok {
		return fmt.Errorf("too early: give-up unlocks after %s more work; hints first", dur(wait))
	}
	t.Outcome = "gaveup"
	finish(s, t, now())
	fmt.Fprintf(out, "GAVE UP after %s of work. It comes back cold in %d days.\n", dur(t.Worked()), cfg().RetryResolve)
	if t.Kind == "new" {
		fmt.Fprintf(out, "(first re-solve in %d days)\n", cfg().FirstResolve)
	}
	revealWhy(t)
	if t.Source() == "cf" {
		fmt.Fprintf(out, "editorial: the contest's Tutorial link at https://codeforces.com/contest/%s\n", contestOf(t.ID))
	} else if t.Source() == "ac" {
		fmt.Fprintf(out, "editorial: https://atcoder.jp/contests/%s/editorial\n", acContest(t.ID))
	} else if statement != "" {
		fmt.Fprintf(out, "statement:\n%s\n", statement)
	}
	if p := solutionPath(t); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Fprintf(out, "their attempt (%s):\n%s\n", p, b)
		}
	}
	return nil
}

func revealWhy(t *Task) {
	if len(t.Tags) > 0 {
		fmt.Fprintf(out, "topic: %s (tags: %s)\n", orAny(t.Focus), strings.Join(t.Tags, ", "))
	}
	if w := why[t.Focus]; w != "" {
		fmt.Fprintln(out, "why it matters:", w)
	}
}

func orAny(f string) string {
	if f == "" {
		return "any"
	}
	return f
}

// contestOf is the leading digits of a CF id: "cf:1360E1" → "1360".
func contestOf(id string) string {
	s := strings.TrimPrefix(id, "cf:")
	if i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		return s[:i]
	}
	return s
}

// skip drops the current new task. The skip is kept even if no next problem
// can be picked right now.
func skip(s *State, seen bool, cat *catalog) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	if t.Kind != "new" && t.Kind != "upsolve" {
		return fmt.Errorf("re-solves can't be skipped; `edge giveup` reschedules it")
	}
	if seen {
		s.Seen[t.ID] = true
	}
	t.Outcome = "skipped"
	t.Finished = now()
	s.History = append(s.History, *t)
	if t.Kind == "upsolve" {
		s.upsolveDone(t)
	}
	s.Current = nil
	if _, err := assign(s, cat); err != nil {
		fmt.Fprintln(out, "skipped; no next problem yet:", err)
	}
	return nil
}

// shelve puts back a task that was offered on an earlier day and never
// started, so a fresh one can come: nothing sits pending for days. A new
// problem is recorded as ignored; a re-solve comes back tomorrow.
// Upsolves are never shelved (stale says no): they have their own due date.
func shelve(s *State) {
	t := s.Current
	if t != nil && t.Kind == "new" {
		t.Outcome, t.Finished = "ignored", now()
		s.History = append(s.History, *t)
	}
	putBack(s, day(now()).AddDate(0, 0, 1))
}

// putBack takes the current unstarted task off without a trace in the
// history and returns it to where it came from: a re-solve goes back to the
// probes, due at due; an upsolve stays owed on its round; a new problem or a
// warm-up can simply be picked again. Nothing is lost.
func putBack(s *State, due time.Time) {
	t := s.Current
	if t == nil {
		return
	}
	s.Current = nil
	s.DriftAt = time.Time{}
	if t.Kind == "resolve" && !slices.ContainsFunc(s.Probes, func(p Probe) bool { return p.Problem == t.ID }) {
		s.Probes = append(s.Probes, Probe{Problem: t.ID, Title: t.Title, URL: t.URL, Rating: t.Rating,
			Due: due, Stage: t.ProbeStage, Try: t.ProbeTry, Policy: t.Arm})
	}
	logEvent("abandon", map[string]any{"task": t.ID, "kind": t.Kind, "why": "put back"})
}

// finish records the task and schedules the cold re-solves that decide
// whether it stuck. Every new problem gets its first re-solve after the
// same delay, whatever the outcome, so the hint experiment compares like
// with like; passing it earns one more, later; failing repeats the stage.
func finish(s *State, t *Task, at time.Time) {
	t.Finished = at
	s.History = append(s.History, *t)
	s.Current = nil
	s.DriftAt = time.Time{}
	c := cfg()
	probe := func(days, stage, try int) {
		s.Probes = append(s.Probes, Probe{Problem: t.ID, Title: t.Title, URL: t.URL, Rating: t.Rating,
			Due: day(now()).AddDate(0, 0, days), Stage: stage, Try: try, Policy: t.Arm})
	}
	switch t.Kind {
	case "new":
		if !t.Known {
			probe(c.FirstResolve, 1, 0)
		}
		s.adapt(t.Source())
	case "upsolve":
		s.upsolveDone(t)
		probe(c.FirstResolve, 1, 0) // a contest problem sticks or it doesn't, like any other
	case "resolve":
		switch {
		case t.Outcome != "solo" && t.ProbeTry+1 >= maxTries:
			fmt.Fprintf(out, "%s failed %d cold re-solves at this stage; dropping it.\n", t.Title, maxTries)
		case t.Outcome != "solo":
			probe(c.RetryResolve, t.ProbeStage, t.ProbeTry+1)
		case t.ProbeStage == 1:
			probe(c.SecondResolve, 2, 0)
		}
	}
}

// lastSolved is the most recent accepted task still waiting for its explain-back.
func (s *State) lastSolved() *Task {
	for i := len(s.History) - 1; i >= 0; i-- {
		t := &s.History[i]
		if t.Outcome == "solo" || t.Outcome == "assisted" {
			if t.Explain < 0 {
				return t
			}
			return nil
		}
	}
	return nil
}

func explainInfo(s *State) error {
	t := s.lastSolved()
	if t == nil {
		return fmt.Errorf("nothing waiting for an explain-back")
	}
	fmt.Fprintf(out, "EXPLAIN-BACK for %s %s (%s, %d hints, %s of work)\n", t.Title, t.URL, t.Outcome, t.Hints, dur(t.Worked()))
	revealWhy(t)
	if p := solutionPath(t); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Fprintf(out, "their accepted code (%s):\n%s\n", p, b)
		}
	}
	fmt.Fprintln(out, "Ask 2 'why' questions, grade 0-2, then `edge explain <grade>`.")
	return nil
}

func explain(s *State, score int) error {
	t := s.lastSolved()
	if t == nil {
		return fmt.Errorf("nothing waiting for an explain-back")
	}
	t.Explain = max(0, min(2, score))
	if t.Explain == 0 {
		if !slices.ContainsFunc(s.Probes, func(p Probe) bool { return p.Problem == t.ID }) {
			s.Probes = append(s.Probes, Probe{Problem: t.ID, Title: t.Title, URL: t.URL, Rating: t.Rating,
				Due: day(now()).AddDate(0, 0, cfg().RetryResolve), Stage: 1, Policy: t.Arm})
		}
		fmt.Fprintln(out, "Couldn't defend it, so it comes back as a cold re-solve.")
	}
	fmt.Fprintf(out, "recorded %d/2 for %s\n", t.Explain, t.Title)
	return nil
}

// dur prints a duration in minutes or hours: "45m", "1h05m".
func dur(d time.Duration) string {
	d = max(0, d).Round(time.Minute)
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("%dh%02dm", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
