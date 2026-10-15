package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// assign picks the next task and makes it current, not yet started: the
// clock starts when the user begins (Start button, the file appearing, or
// a first submission), so hint timers measure real work.
func assign(s *State) (*Task, error) {
	if now().Sub(s.LastSync) > 6*time.Hour {
		if err := sync(s); err != nil {
			fmt.Fprintln(os.Stderr, "edge: sync failed, using cached data:", err)
		}
	}
	cf, err := cfProblems()
	if err != nil {
		return nil, err
	}
	t := &Task{}
	switch {
	case s.gapDays() >= 2:
		if p := s.pickReentry(cf); p != nil {
			*t = Task{ID: p.ID, Kind: "reentry", Title: p.Title, URL: p.URL, Rating: p.Rating, Tags: p.Tags, Policy: "cold"}
			break
		}
		fallthrough
	default:
		if i := s.dueProbe(); i >= 0 {
			p := s.Probes[i]
			*t = Task{ID: p.Problem, Kind: "resolve", Title: p.Title, URL: p.URL, Rating: p.Rating,
				Policy: "cold", Arm: p.Policy, ProbeOf: p.Problem, ProbeStage: p.Stage}
			s.Probes = slices.Delete(s.Probes, i, i+1)
			break
		}
		if err := s.newTask(t, cf); err != nil {
			return nil, err
		}
	}
	t.Assigned, t.Explain = now(), -1
	s.Current = t
	return t, nil
}

func (s *State) newTask(t *Task, cf []Problem) error {
	src := s.nextSource()
	probs := cf
	focus := focusCF
	if src == "lc" {
		lc, err := lcProblems()
		if err != nil {
			fmt.Fprintln(os.Stderr, "edge: leetcode unavailable, using codeforces:", err)
			src = "cf"
		} else {
			probs, focus = lc, focusLC
		}
	}
	solved := s.focusCounts(src, probs)
	for try := 0; try < 20; try++ {
		f := pickFocus(focus, solved)
		if p := s.pickNew(src, probs, f); p != nil {
			*t = Task{ID: p.ID, Kind: "new", Title: p.Title, URL: p.URL, Rating: p.Rating, Tags: p.Tags,
				Focus: f, Policy: s.pickPolicy()}
			t.Arm = t.Policy
			return nil
		}
	}
	lo, hi := s.window(src)
	return fmt.Errorf("no unseen %s problem in the %d-%d band", src, lo, hi)
}

// begin starts the clock and prepares the solution file.
func begin(t *Task, at time.Time) {
	if t.Active() {
		return
	}
	t.Started = at
	if _, err := prepare(t); err != nil {
		fmt.Fprintln(os.Stderr, "edge: couldn't prepare the solution file:", err)
	}
}

func describe(s *State, t *Task) string {
	var b strings.Builder
	label := map[string]string{"new": "NEW", "resolve": "COLD RE-SOLVE", "reentry": "WARM-UP (welcome back)"}[t.Kind]
	fmt.Fprintf(&b, "%s  %s\n  %s\n  rating %d", label, t.Title, t.URL, t.Rating)
	if t.Kind == "new" {
		fmt.Fprintf(&b, " · hints: %s (topic revealed after you solve it)\n", t.Policy)
	} else {
		b.WriteString(" · no hints, from scratch, no peeking at your old code\n")
	}
	if t.Active() {
		fmt.Fprintf(&b, "  started %s ago · %d rejected\n", since(t.Started), t.Fails)
	} else {
		b.WriteString("  not started yet\n")
	}
	got, need := s.today()
	fmt.Fprintf(&b, "today %d/%d · week %d/%d\n", got, need, s.creditsSince(weekStart(now())), weeklyTarget)
	return b.String()
}

func stuck(s *State) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	begin(t, now())
	fmt.Printf("problem: %s %s (rating %d, tags hidden) · %s in, %d rejected submissions\n",
		t.Title, t.URL, t.Rating, since(t.Started), t.Fails)
	if t.Source() == "lc" {
		if st, err := lcStatement(strings.TrimPrefix(t.ID, "lc:")); err == nil {
			fmt.Printf("statement:\n%s\n", st)
		}
	}
	if p := solutionPath(t); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Printf("their current code (%s):\n%s\n", p, b)
		}
	}
	lvl, wait := hintAllowance(t)
	if lvl <= t.Hints {
		if t.Policy == "cold" || t.Policy == "" {
			fmt.Println("HINTS: none. This is a cold re-solve. If they can't do it, `edge giveup` reschedules it.")
		} else if t.Hints >= 3 {
			fmt.Println("HINTS: all 3 used. Next step is `edge giveup` (full solution) when allowed.")
		} else {
			fmt.Printf("HINTS: locked. Next hint unlocks in %s.\n", wait.Round(time.Minute))
		}
		return nil
	}
	t.Hints++
	fmt.Printf("HINT LEVEL %d granted (%d of 3 used)\n", t.Hints, t.Hints)
	return nil
}

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
	_, recent, err := lcProfile()
	if err != nil {
		return false, err
	}
	for _, a := range recent {
		ts, _ := strconv.ParseInt(a.Time, 10, 64)
		if "lc:"+a.Slug == t.ID && ts >= from {
			return true, nil
		}
	}
	return false, nil
}

// accept records an accepted solution.
func accept(s *State, t *Task, at time.Time) {
	begin(t, at)
	t.Outcome = "solo"
	if t.Hints > 0 {
		t.Outcome = "assisted"
	}
	finish(s, t, at)
}

func done(s *State) error {
	t := s.Current
	if t == nil {
		// The daemon usually records the AC first; then this is just the explain-back.
		return explainInfo(s)
	}
	ok, err := verifyAC(t)
	if err != nil {
		return fmt.Errorf("couldn't verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("no accepted submission for %s yet; submit it first", t.Title)
	}
	accept(s, t, now())
	fmt.Printf("ACCEPTED (%s) in %s\n", t.Outcome, since(t.Started))
	return explainInfo(s)
}

func giveup(s *State) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	begin(t, now())
	if ok, wait := canGiveUp(t); !ok {
		return fmt.Errorf("too early: give-up unlocks in %s; hints first", wait.Round(time.Minute))
	}
	t.Outcome = "gaveup"
	finish(s, t, now())
	fmt.Printf("GAVE UP after %s. It comes back cold in 3 days.\n", since(t.Started))
	revealWhy(t)
	if t.Source() == "cf" {
		fmt.Printf("editorial: the contest's Tutorial link at https://codeforces.com/contest/%s\n", contestOf(t.ID))
	} else {
		fmt.Printf("statement:\n")
		if st, err := lcStatement(strings.TrimPrefix(t.ID, "lc:")); err == nil {
			fmt.Println(st)
		}
	}
	if p := solutionPath(t); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Printf("their attempt (%s):\n%s\n", p, b)
		}
	}
	return nil
}

func revealWhy(t *Task) {
	if t.Focus == "" {
		return
	}
	fmt.Printf("topic: %s (tags: %s)\n", t.Focus, strings.Join(t.Tags, ", "))
	if w := why[t.Focus]; w != "" {
		fmt.Println("why it matters:", w)
	}
}

// contestOf is the leading digits of a CF id: "cf:1360E1" → "1360".
func contestOf(id string) string {
	s := strings.TrimPrefix(id, "cf:")
	if i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		return s[:i]
	}
	return s
}

func skip(s *State, seen bool) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	if t.Kind != "new" {
		return fmt.Errorf("re-solves can't be skipped; `edge giveup` reschedules it")
	}
	if seen {
		s.Seen[t.ID] = true
	}
	t.Outcome = "skipped"
	t.Finished = now()
	s.History = append(s.History, *t)
	s.Current = nil
	_, err := assign(s)
	return err
}

// finish records the task and schedules the cold re-solves that decide
// whether it stuck.
func finish(s *State, t *Task, at time.Time) {
	t.Finished = at
	s.History = append(s.History, *t)
	s.Current = nil
	probe := func(days, stage int) {
		s.Probes = append(s.Probes, Probe{Problem: t.ID, Title: t.Title, URL: t.URL, Rating: t.Rating,
			Due: day(now()).AddDate(0, 0, days), Stage: stage, Policy: t.Arm})
	}
	switch t.Kind {
	case "new":
		if t.Outcome == "solo" {
			probe(14, 3)
		} else {
			probe(3, 1)
		}
		s.adapt(t.Source())
	case "resolve":
		switch {
		case t.Outcome != "solo":
			probe(3, t.ProbeStage) // failed: same stage again
		case t.ProbeStage == 1:
			probe(10, 2)
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
	fmt.Printf("EXPLAIN-BACK for %s %s (%s, %d hints, %s)\n", t.Title, t.URL, t.Outcome, t.Hints, t.Finished.Sub(t.Started).Round(time.Minute))
	revealWhy(t)
	if p := solutionPath(t); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Printf("their accepted code (%s):\n%s\n", p, b)
		}
	}
	fmt.Println("Ask 2 'why' questions, grade 0-2, then `edge explain <grade>`.")
	return nil
}

func explain(s *State, score int) error {
	t := s.lastSolved()
	if t == nil {
		return fmt.Errorf("nothing waiting for an explain-back")
	}
	t.Explain = max(0, min(2, score))
	if t.Explain == 0 && !slices.ContainsFunc(s.Probes, func(p Probe) bool { return p.Problem == t.ID && p.Stage == 1 }) {
		s.Probes = append(s.Probes, Probe{Problem: t.ID, Title: t.Title, URL: t.URL, Rating: t.Rating,
			Due: day(now()).AddDate(0, 0, 3), Stage: 1, Policy: t.Arm})
		fmt.Println("Couldn't defend it, so it comes back in 3 days.")
	}
	fmt.Println("recorded")
	return nil
}
