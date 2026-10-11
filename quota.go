package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	weekdayMin   = 1
	weekendMin   = 3
	weeklyTarget = 12
	honestTry    = 45 * time.Minute // a give-up after this long still earns the day
)

func day(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func weekStart(t time.Time) time.Time {
	d := day(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // Monday
}

func dailyMin(t time.Time) int {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return weekendMin
	}
	return weekdayMin
}

func credit(t Task) int {
	switch t.Outcome {
	case "solo", "assisted":
		return 1
	case "gaveup":
		if t.Finished.Sub(t.Started) >= honestTry {
			return 1
		}
	}
	return 0
}

func (s *State) creditsSince(from time.Time) int {
	n := 0
	for _, t := range s.History {
		if !t.Finished.Before(from) {
			n += credit(t)
		}
	}
	return n
}

func (s *State) today() (got, need int) {
	return s.creditsSince(day(now())), dailyMin(now())
}

// streak counts consecutive days, ending today or yesterday, that met the minimum.
func (s *State) streak() int {
	d := day(now())
	if got, need := s.today(); got < need {
		d = d.AddDate(0, 0, -1)
	}
	n := 0
	for {
		got := 0
		for _, t := range s.History {
			if day(t.Finished).Equal(d) {
				got += credit(t)
			}
		}
		if got < dailyMin(d) && !s.passOn(d) {
			return n
		}
		n++
		d = d.AddDate(0, 0, -1)
	}
}

func (s *State) passOn(d time.Time) bool {
	for _, p := range s.Passes {
		if day(p).Equal(day(d)) {
			return true
		}
	}
	return false
}

func (s *State) passUsedThisWeek() bool {
	ws := weekStart(now())
	for _, p := range s.Passes {
		if !p.Before(ws) {
			return true
		}
	}
	return false
}

func workDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "CP")
}

type verdict struct {
	Block   bool
	Reason  string
	Context string // injected into Claude's context when allowed
}

// gateDecide is the whole lockout policy, kept pure so it can be tested.
func gateDecide(s *State, prompt, cwd string) verdict {
	p := strings.TrimSpace(prompt)
	inWork := cwd == workDir() || strings.HasPrefix(cwd, workDir()+string(filepath.Separator))
	if strings.HasPrefix(p, "/edge") {
		return verdict{}
	}
	if inWork {
		return verdict{Context: tutorContract(s)}
	}
	got, need := s.today()
	if got >= need || s.passOn(now()) {
		return verdict{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "edge gate: today's learning isn't done (%d/%d).", got, need)
	if s.Current != nil {
		fmt.Fprintf(&b, "\nYou're on: %s (%s), started %s ago.", s.Current.Title, s.Current.URL, since(s.Current.Started))
	} else {
		b.WriteString("\nRun /edge-next to get your task.")
	}
	b.WriteString("\nWork in ~/CP. /edge-stuck for hints, /edge-done when accepted.")
	if !s.passUsedThisWeek() {
		b.WriteString("\nReal emergency? `edge pass` unlocks today (1 per week).")
	}
	return verdict{Block: true, Reason: b.String()}
}

// tutorContract keeps Claude from doing the work when asked directly in the
// learning workspace, outside /edge-stuck.
func tutorContract(s *State) string {
	c := "edge tutor mode: the user is practicing DSA in C++. Never write the solution or the key idea for their current problem. " +
		"If they ask for help on it, tell them to run /edge-stuck, which gives hints at the level they have unlocked. " +
		"Explaining general concepts, C++ syntax, STL or compiler errors is fine."
	if t := s.Current; t != nil {
		c += fmt.Sprintf(" Current problem: %s %s (policy %s, %d hints used).", t.Title, t.URL, t.Policy, t.Hints)
	}
	return c
}

func since(t time.Time) string {
	d := now().Sub(t).Round(time.Minute)
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
