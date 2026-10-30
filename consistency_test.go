package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nov is a time in November 2026, Trial 1's month: Fri 6 is its first day,
// Mon 9 the next week, Fri 20 Trial 2's first day.
func nov(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.Local) }

// worked adds a finished task with min focused minutes on its day.
func worked(s *State, id string, d, min int, outcome string) *Task {
	start := nov(d, 20, 0)
	s.History = append(s.History, Task{ID: id, Kind: "new", Title: id, Outcome: outcome, Started: start,
		Finished: start.Add(time.Duration(min) * time.Minute), WorkSec: min * 60,
		WorkDays: map[string]int{dateKey(day(start)): min * 60}})
	return &s.History[len(s.History)-1]
}

// rep gives day d a rep: an accepted solution.
func rep(s *State, d int) { worked(s, "cf:"+strings.Repeat("A", d), d, 30, "solo") }

// consState is a fresh state with Trial 1 on, at t.
func consState(t *testing.T, at0 time.Time) *State {
	t.Helper()
	at(t, at0)
	return fresh(t)
}

// What makes a day's rep: a submit, 25 focused minutes, an AC, a round sat,
// or a give-up after 25 minutes. In the trial phases today() is that rep,
// one a day, weekends included; the baseline keeps its quota.
func TestRepRules(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(s *State)
		kind string
	}{
		{"nothing", func(s *State) {}, ""},
		{"24 focused minutes", func(s *State) { worked(s, "cf:1A", 9, 24, "skipped") }, ""},
		{"25 focused minutes", func(s *State) { worked(s, "cf:1A", 9, 25, "skipped") }, "focus"},
		{"two tasks, 25 minutes", func(s *State) { worked(s, "cf:1A", 9, 9, "skipped"); worked(s, "cf:1B", 9, 16, "ignored") }, "focus"},
		{"a submit", func(s *State) { pending(s).SubSeen = nov(9, 20, 10).Unix() }, "submit"},
		{"an AC", func(s *State) { worked(s, "cf:1A", 9, 12, "solo") }, "ac"},
		{"a give-up after 30", func(s *State) { worked(s, "cf:1A", 9, 30, "gaveup") }, "giveup"},
		{"a give-up after 10", func(s *State) { worked(s, "cf:1A", 9, 10, "gaveup") }, ""},
		{"a round", func(s *State) {
			s.Rounds = append(s.Rounds, Round{ID: "cf:2272", Start: nov(9, 20, 5), DurSec: 7200, Joined: "live"})
		}, "round"},
		{"yesterday's AC", func(s *State) { worked(s, "cf:1A", 8, 30, "solo") }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := consState(t, nov(9, 23, 0))
			c.set(s)
			cs := s.chain()
			if cs.Kind != c.kind {
				t.Fatalf("kind %q, want %q", cs.Kind, c.kind)
			}
			got, need := s.today()
			if want := map[bool]int{true: 1, false: 0}[c.kind != ""]; got != want || need != 1 {
				t.Fatalf("today %d/%d", got, need)
			}
		})
	}
	s := consState(t, nov(7, 15, 0)) // Saturday: one rep too
	if got, need := s.today(); got != 0 || need != 1 || s.dailyMin(now()) != 1 {
		t.Fatalf("weekend: %d/%d", got, need)
	}
	s = consState(t, wed) // baseline: the quota as before
	worked(s, "cf:1A", 0, 30, "skipped")
	if got, need := s.today(); got != 0 || need != 1 {
		t.Fatalf("baseline: %d/%d", got, need)
	}
}

// The gate reads the rep: closed in study time until it counts.
func TestGateHoldsToTheRep(t *testing.T) {
	s := consState(t, nov(9, 21, 0))
	h, _ := os.UserHomeDir()
	other := filepath.Join(h, "Gems/kinvault")
	if v := gateDecide(s, "fix the bug", other); !v.Block {
		t.Fatal("closed before the rep")
	}
	pending(s)
	begin(s, s.Current, nov(9, 20, 30))
	s.Current.addWork(nov(9, 20, 55), 25*60)
	if v := gateDecide(s, "fix the bug", other); v.Block {
		t.Fatalf("25 focused minutes open it: %s", v.Reason)
	}
}

// The chain: rep days in a row, one miss forgiven, two in a row break it,
// a token day neutral, today pending until its rep. Never "0" on screen:
// after a break the next rep is 1.
func TestChain(t *testing.T) {
	for _, c := range []struct {
		name          string
		reps, tokens  []int
		today         int
		chain, missed int
	}{
		{"three in a row, today pending", []int{6, 7, 8}, nil, 9, 3, 0},
		{"one miss is forgiven", []int{6, 7, 9}, nil, 10, 3, 0},
		{"yesterday missed", []int{6, 7, 8}, nil, 10, 3, 1},
		{"two misses break it", []int{6, 7}, nil, 10, 0, 0},
		{"an old break", []int{3, 6, 7}, nil, 9, 2, 1},
		{"a token is neutral", []int{6, 8}, []int{7}, 9, 2, 0},
		{"a token doesn't hide a second miss", []int{6}, []int{8}, 10, 0, 0},
		{"nothing yet", nil, nil, 9, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := consState(t, nov(c.today, 21, 0))
			for _, d := range c.reps {
				rep(s, d)
			}
			for _, d := range c.tokens {
				s.Cons.Tokens = append(s.Cons.Tokens, dateKey(nov(d, 12, 0)))
			}
			cs := s.chain()
			if cs.Chain != c.chain || cs.Missed != c.missed || cs.RepDone {
				t.Fatalf("chain %d missed %d done %v, want %d %d", cs.Chain, cs.Missed, cs.RepDone, c.chain, c.missed)
			}
			if s.streak() != c.chain {
				t.Fatalf("streak %d is the chain in the trial", s.streak())
			}
			rep(s, c.today)
			if cs := s.chain(); cs.Chain != c.chain+1 || !cs.RepDone || cs.Missed != c.missed {
				t.Fatalf("after today's rep: %+v", cs)
			}
		})
	}
}

// Never miss twice: the day after a miss, 20 focused minutes count, and the
// daemon latches that rep so it stays a rep tomorrow.
func TestNeverMissTwice(t *testing.T) {
	s := consState(t, nov(10, 21, 0))
	rep(s, 6)
	rep(s, 7)
	rep(s, 8) // 9 missed
	pending(s)
	begin(s, s.Current, nov(10, 20, 30))
	s.Current.addWork(nov(10, 20, 50), 20*60)
	cs := s.chain()
	if !cs.RepDone || cs.Kind != "focus" || cs.Chain != 4 || cs.Missed != 1 {
		t.Fatalf("20 minutes after a miss: %+v", cs)
	}
	consistencyEvents(s, window{}, now())
	if r := s.Cons.rec("2026-11-10"); r == nil || !r.Rep || r.RepFocus != 20 || r.StartAt.IsZero() {
		t.Fatalf("latched: %+v", r)
	}
	at(t, nov(11, 21, 0))
	if cs := s.chain(); cs.Chain != 4 || cs.Missed != 0 {
		t.Fatalf("next day: %+v", cs)
	}
	rep(s, 11)
	at(t, nov(12, 21, 0)) // a normal day: 25 again
	s.Current.addWork(nov(12, 20, 50), 20*60)
	if cs := s.chain(); cs.RepDone {
		t.Fatalf("20 minutes on a normal day: %+v", cs)
	}
}

// The status carries the chain fields only in the trial phases.
func TestConsistencyStatusOnlyInTrials(t *testing.T) {
	s := consState(t, nov(5, 21, 0))
	rep(s, 3)
	rep(s, 4)
	if st := edgeStatus(s); st.Chain != 0 || st.SkipTokens != 0 || st.RepDone || st.CueMs != 0 || st.Phase != "baseline" {
		t.Fatalf("baseline: %+v", st)
	}
	at(t, nov(6, 21, 0))
	rep(s, 5)
	st := edgeStatus(s)
	if st.Phase != "trial1" || st.Chain != 3 || st.SkipTokens != 1 || st.RepDone || st.Today != 0 || st.Need != 1 || st.Streak != 3 {
		t.Fatalf("trial1: %+v", st)
	}
}
