// edge keeps you at the edge of what you can solve: one next task, verified
// completion, cold re-solves, and a Claude Code gate until the day's work is done.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "gate" {
		gate() // must never fail closed on its own errors
		return
	}
	if cmd == "install" {
		check(install())
		return
	}
	s, err := load()
	check(err)
	switch cmd {
	case "sync":
		check(sync(s))
		fmt.Printf("synced: cf %d, lc %d, %d problems already solved on CF\n", s.Ratings["cf"], s.Ratings["lc"], len(s.CFDone))
	case "next":
		err = next(s)
	case "stuck":
		err = stuck(s)
	case "done":
		err = done(s)
	case "giveup":
		err = giveup(s)
	case "skip":
		err = skip(s, slices.Contains(args, "--seen"))
	case "explain":
		if len(args) != 1 {
			usage()
		}
		n, _ := strconv.Atoi(args[0])
		err = explain(s, n)
	case "status":
		fmt.Print(status(s))
	case "report":
		fmt.Print(s.experimentReport())
	case "pass":
		err = pass(s)
	default:
		usage()
	}
	check(err)
	check(s.save())
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: edge sync|next|stuck|done|giveup|skip [--seen]|explain <0-2>|status|report|pass|install|gate")
	os.Exit(2)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "edge:", err)
		os.Exit(1)
	}
}

func sync(s *State) error {
	r, err := cfRating()
	if err != nil {
		return err
	}
	s.Ratings["cf"] = r
	subs, err := cfSubs(0)
	if err != nil {
		return err
	}
	for _, x := range subs {
		k := x.Problem.key()
		s.CFTried[k] = true
		if x.Verdict == "OK" {
			if at, ok := s.CFDone[k]; !ok || x.Time < at {
				s.CFDone[k] = x.Time
			}
		}
	}
	lr, recent, err := lcProfile()
	if err != nil {
		return err
	}
	if lr > 0 {
		s.Ratings["lc"] = lr
	}
	for _, a := range recent {
		s.Seen["lc:"+a.Slug] = true
	}
	s.LastSync = now()
	return nil
}

func next(s *State) error {
	if t := s.Current; t != nil {
		fmt.Print(describe(s, t))
		return nil
	}
	if now().Sub(s.LastSync) > 6*time.Hour {
		if err := sync(s); err != nil {
			fmt.Fprintln(os.Stderr, "edge: sync failed, using cached data:", err)
		}
	}
	cf, err := cfProblems()
	if err != nil {
		return err
	}
	t := &Task{Started: now(), Explain: -1}
	switch {
	case s.gapDays() >= 2:
		if p := s.pickReentry(cf); p != nil {
			*t = Task{ID: p.ID, Kind: "reentry", Title: p.Title, URL: p.URL, Rating: p.Rating, Tags: p.Tags,
				Policy: "cold", Started: now(), Explain: -1}
			break
		}
		fallthrough
	default:
		if i := s.dueProbe(); i >= 0 {
			p := s.Probes[i]
			*t = Task{ID: p.Problem, Kind: "resolve", Title: p.Title, URL: p.URL, Rating: p.Rating,
				Policy: "cold", Arm: p.Policy, ProbeOf: p.Problem, ProbeStage: p.Stage, Started: now(), Explain: -1}
			s.Probes = slices.Delete(s.Probes, i, i+1)
			break
		}
		if err := s.newTask(t, cf); err != nil {
			return err
		}
	}
	s.Current = t
	if path, err := scaffold(t); err == nil {
		fmt.Println("file:", path)
	} else {
		fmt.Fprintln(os.Stderr, "edge: couldn't create the solution file:", err)
	}
	fmt.Print(describe(s, t))
	return nil
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
				Focus: f, Policy: s.pickPolicy(), Started: now(), Explain: -1}
			t.Arm = t.Policy
			return nil
		}
	}
	lo, hi := s.window(src)
	return fmt.Errorf("no unseen %s problem in the %d-%d band", src, lo, hi)
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
	fmt.Fprintf(&b, "  started %s ago\n", since(t.Started))
	got, need := s.today()
	fmt.Fprintf(&b, "today %d/%d · week %d/%d\n", got, need, s.creditsSince(weekStart(now())), weeklyTarget)
	return b.String()
}

func stuck(s *State) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task, run `edge next`")
	}
	fmt.Printf("problem: %s %s (rating %d, tags hidden)\n", t.Title, t.URL, t.Rating)
	if t.Source() == "lc" {
		if st, err := lcStatement(strings.TrimPrefix(t.ID, "lc:")); err == nil {
			fmt.Printf("statement:\n%s\n", st)
		}
	}
	lvl, wait := hintAllowance(t)
	if lvl <= t.Hints {
		if t.Policy == "cold" || t.Policy == "" {
			fmt.Println("HINTS: none. This is a cold re-solve. If you can't do it, `edge giveup` reschedules it.")
		} else {
			fmt.Printf("HINTS: locked. Next hint unlocks in %s. Keep going: write down what you've tried.\n", wait.Round(time.Minute))
		}
		return nil
	}
	t.Hints++
	fmt.Printf("HINT LEVEL %d granted (%d of 3 used)\n", t.Hints, t.Hints)
	return nil
}

func verifyAC(t *Task) (bool, error) {
	from := t.Started.Add(-time.Minute).Unix()
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

func done(s *State) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	ok, err := verifyAC(t)
	if err != nil {
		return fmt.Errorf("couldn't verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("no accepted submission for %s since you started; submit it first", t.Title)
	}
	t.Outcome = "solo"
	if t.Hints > 0 {
		t.Outcome = "assisted"
	}
	finish(s, t)
	fmt.Printf("ACCEPTED (%s) in %s\n", t.Outcome, since(t.Started))
	revealWhy(t)
	fmt.Println("EXPLAIN-BACK: answer 2 'why' questions, then `edge explain <0-2>`.")
	return nil
}

func giveup(s *State) error {
	t := s.Current
	if t == nil {
		return fmt.Errorf("no current task")
	}
	if ok, wait := canGiveUp(t); !ok {
		return fmt.Errorf("too early: give-up unlocks in %s; use /edge-stuck for hints", wait.Round(time.Minute))
	}
	t.Outcome = "gaveup"
	finish(s, t)
	fmt.Printf("GAVE UP after %s. It comes back cold in 3 days.\n", since(t.Started))
	revealWhy(t)
	if t.Source() == "cf" {
		fmt.Printf("editorial: the contest's Tutorial link at https://codeforces.com/contest/%s\n", contestOf(t.ID))
	} else {
		fmt.Printf("editorial: %seditorial/\n", t.URL)
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

func contestOf(id string) string {
	s := strings.TrimPrefix(id, "cf:")
	return strings.TrimRightFunc(s, func(r rune) bool { return r < '0' || r > '9' })
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
	return next(s)
}

// finish records the task and schedules the cold re-solves that decide
// whether it stuck.
func finish(s *State, t *Task) {
	t.Finished = now()
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

func explain(s *State, score int) error {
	for i := len(s.History) - 1; i >= 0; i-- {
		t := &s.History[i]
		if t.Outcome == "solo" || t.Outcome == "assisted" {
			t.Explain = max(0, min(2, score))
			if t.Explain == 0 && !slices.ContainsFunc(s.Probes, func(p Probe) bool { return p.Problem == t.ID }) {
				s.Probes = append(s.Probes, Probe{Problem: t.ID, Title: t.Title, URL: t.URL, Rating: t.Rating,
					Due: day(now()).AddDate(0, 0, 3), Stage: 1, Policy: t.Arm})
				fmt.Println("Couldn't defend it, so it comes back in 3 days.")
			}
			return nil
		}
	}
	return fmt.Errorf("nothing solved to explain")
}

func pass(s *State) error {
	if s.passOn(now()) {
		return fmt.Errorf("already passed today")
	}
	if s.passUsedThisWeek() {
		return fmt.Errorf("this week's pass is used; do one task")
	}
	s.Passes = append(s.Passes, now())
	fmt.Println("gate open for today. It's logged.")
	return nil
}

func status(s *State) string {
	var b strings.Builder
	got, need := s.today()
	fmt.Fprintf(&b, "today %d/%d · week %d/%d · streak %d days\n", got, need, s.creditsSince(weekStart(now())), weeklyTarget, s.streak())
	for _, src := range []string{"cf", "lc"} {
		lo, hi := s.window(src)
		fmt.Fprintf(&b, "%s rating %d → working band %d-%d\n", src, s.Ratings[src], lo, hi)
	}
	if t := s.Current; t != nil {
		fmt.Fprintf(&b, "current: %s (%s ago)\n", t.Title, since(t.Started))
	}
	due := 0
	for _, p := range s.Probes {
		if !p.Due.After(now()) {
			due++
		}
	}
	fmt.Fprintf(&b, "re-solves: %d due, %d scheduled\n", due, len(s.Probes))
	if !gateInstalled() {
		s.GateOff = append(s.GateOff, now())
		fmt.Fprintf(&b, "GATE IS OFF: the hook is missing from ~/.claude/settings.json (seen off %d times)\n", len(s.GateOff))
	} else if !s.GateLast.IsZero() {
		fmt.Fprintf(&b, "gate: last ran %s ago\n", since(s.GateLast))
	}
	b.WriteString(s.experimentReport())
	return b.String()
}

// ---- hook ----

func gate() {
	var in struct {
		Prompt string `json:"prompt"`
		Cwd    string `json:"cwd"`
	}
	json.NewDecoder(os.Stdin).Decode(&in)
	s, err := load()
	if err != nil {
		return // fail open: a broken state file must not lock you out of Claude
	}
	v := gateDecide(s, in.Prompt, in.Cwd)
	s.GateRuns++
	s.GateLast = now()
	s.save()
	switch {
	case v.Block:
		json.NewEncoder(os.Stdout).Encode(map[string]any{"decision": "block", "reason": v.Reason})
	case v.Context != "":
		json.NewEncoder(os.Stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "UserPromptSubmit", "additionalContext": v.Context}})
	}
}

func settingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "settings.json")
}

func gateInstalled() bool {
	b, err := os.ReadFile(settingsPath())
	return err == nil && strings.Contains(string(b), "edge gate")
}
