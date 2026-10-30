package main

// Tests from the edge audit (~/Gems/ember/.audit/edge-bugs.md), one per
// bug, kept as acceptance tests for the fixes.

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// actionEnv makes onAction safe to call: launches run `true`, not a browser.
func actionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NIRI_SOCKET", "")
	writeConfig(t, "[launch]\nterminal = \"true\"\neditor = \"true\"\nbrowser = \"true\"\n")
	old := catalogFn
	catalogFn = func() *catalog { return &catalog{err: map[string]error{"cf": os.ErrNotExist}} } // offline
	t.Cleanup(func() { catalogFn = old })
}

// logs captures edge's log during f.
func logs(f func()) string {
	var b bytes.Buffer
	old := log.Writer()
	log.SetOutput(&b)
	defer log.SetOutput(old)
	f()
	return b.String()
}

// missedRound is a round held and missed: it settles owing C and D.
func missedRound(s *State, id string, start time.Time) *Round {
	s.Rounds = append(s.Rounds, Round{ID: id, Name: "Codeforces Round 1 (Div. 2)", Kind: "div2", Start: start, DurSec: 7200, Plan: "out",
		Problems: []RoundProblem{{ID: id + "A", Index: "A"}, {ID: id + "B", Index: "B"}, {ID: id + "C", Index: "C"}, {ID: id + "D", Index: "D"}}})
	r := &s.Rounds[len(s.Rounds)-1]
	s.settle(r)
	return r
}

// B5: an upsolve that's current but not started lapses on its round and
// stays current: edge keeps offering a lapsed problem ("Upsolve CF 1128 C:
// the round's next problem"), and Start on it works a problem the round
// already counts as lapsed. Live on 26 Oct: cf:2261C lapsed at 04:00,
// started 18:40, worked ~19 min, skipped 27 Oct 18:56.
func TestAuditLapsedUpsolveStaysCurrent(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	r := missedRound(s, "cf:2261", wed.AddDate(0, 0, -1))
	if _, err := assign(s, nil); err != nil || s.Current == nil || s.Current.Kind != "upsolve" {
		t.Fatalf("assign: %v %+v", err, s.Current)
	}
	id := s.Current.ID
	at(t, wed.AddDate(0, 0, cfg().UpsolveDays+2)) // past its due, never started
	s.tidyRounds()
	if r.letters("lapsed") == "" {
		t.Fatalf("lapse: %+v", r.Problems)
	}
	if c := s.Current; c != nil && c.ID == id {
		t.Errorf("%s lapsed on the round but is still the current task", id)
	}
	if k := keys(plan(s, window{}, 0)); strings.Contains(k, "offer:"+id) {
		t.Errorf("a lapsed upsolve is still offered: %s", k)
	}
}

// B6: "Upsolve now" on a missed round's card isn't tied to that round:
// with another round's upsolve current, it reopens that one. Live at 18:54
// on 27 Oct: CF 1130's card would have reopened CF 1128 C.
func TestAuditUpsolveButtonIgnoresItsRound(t *testing.T) {
	actionEnv(t)
	at(t, wed)
	s := fresh(t)
	missedRound(s, "cf:2261", wed.AddDate(0, 0, -3))
	assign(s, nil) // CF 2261's upsolve is current, not started
	at(t, wed.Add(3*time.Hour))
	y := missedRound(s, "cf:2272", wed)
	var card emberCard
	for _, e := range s.roundEvents(window{}, 0) {
		if strings.HasPrefix(e.Key, "roundmissed:cf:2272") {
			card = emberCardFor(e)
		}
	}
	key := ""
	for _, b := range card.Buttons {
		if strings.HasPrefix(b.Key, "upsolve") {
			key = b.Key
		}
	}
	if key != "upsolve:cf:2272" {
		t.Fatalf("%s's card: button %q doesn't name its round", y.ID, key)
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	onAction(key)
	s2, _ := load()
	if s2.Current == nil || s2.Current.Round != "cf:2272" || !s2.Current.Active() {
		t.Errorf("Upsolve on cf:2272's card: current is %+v", s2.Current)
	}
	if r := s2.round("cf:2261"); r.owed(now()) == nil {
		t.Errorf("CF 2261's upsolve was put back, so it's still owed: %+v", r.Problems)
	}

	// working on another task: the click says so instead of acting on it
	at(t, wed.Add(3*time.Hour+time.Minute))
	if _, err := act("upsolve:cf:2261"); err == nil || !strings.Contains(err.Error(), "finish") {
		t.Errorf("upsolve while another is active: %v", err)
	}
}

// B7: the Upsolve button throws away an unstarted cold re-solve: assign
// took it out of Probes, and onAction("upsolve") sets Current = nil.
func TestAuditUpsolveActionDropsResolve(t *testing.T) {
	actionEnv(t)
	at(t, wed)
	s := fresh(t)
	s.Probes = append(s.Probes, Probe{Problem: "cf:1360E", Title: "Polygon", URL: "u", Rating: 1300, Due: wed.Add(-time.Hour), Stage: 1, Policy: "ladder"})
	if _, err := assign(s, nil); err != nil || s.Current == nil || s.Current.Kind != "resolve" {
		t.Fatalf("assign: %v %+v", err, s.Current)
	}
	missedRound(s, "cf:2272", wed.Add(-4*time.Hour)) // a round now owes upsolves
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	onAction("upsolve")
	s2, _ := load()
	kept := false
	for _, p := range s2.Probes {
		kept = kept || p.Problem == "cf:1360E"
	}
	if !kept {
		t.Errorf("the due re-solve of cf:1360E is gone: probes %+v, current %s", s2.Probes, s2.Current.ID)
	}
}

// B4: Ember's dashboard sends `open` while he's working; edge has no case
// for it: the default branch looks for round "" and logs an error nobody
// sees (`edge action` runs from Ember with its output discarded).
func TestAuditOpenAction(t *testing.T) {
	actionEnv(t)
	at(t, wed)
	s := fresh(t)
	pending(s)
	begin(s, s.Current, now().Add(-10*time.Minute))
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if out := logs(func() { onAction("open") }); strings.Contains(out, "no round") {
		t.Errorf("open: %s", strings.TrimSpace(out))
	}
}

// B8: stats put all of the current task's work on today. A task started
// yesterday evening shows yesterday's minutes as today's, and yesterday
// as 0: the week bars and the "focused today" line are wrong.
func TestAuditTodayWorkFromYesterday(t *testing.T) {
	at(t, wed.Add(-24*time.Hour)) // Tuesday 21:00
	s := fresh(t)
	pending(s)
	begin(s, s.Current, now())
	s.Current.WorkSec = 1131        // all of it on Tuesday
	at(t, wed.Add(-10*time.Minute)) // Wednesday 20:50, not touched today
	st := s.stats()
	tue := st.Calendar[len(st.Calendar)-2]
	if st.Today.WorkSec != 0 || tue.WorkSec != 1131 {
		t.Errorf("today %ds (want 0), Tuesday %ds (want 1131)", st.Today.WorkSec, tue.WorkSec)
	}
}

// B10: a shelved upsolve comes straight back: shelve drops it without a
// trace and the same tick reassigns it with its offer count reset, so it
// is offered every 30 min, day after day (live: cf:2261C, 24-25 Oct).
func TestAuditShelvedUpsolveComesStraightBack(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	missedRound(s, "cf:2261", wed.AddDate(0, 0, -1))
	assign(s, nil)
	s.Current.Offers = 3
	id := s.Current.ID
	at(t, wed.Add(22*time.Hour+5*time.Minute)) // Thursday 19:05: study time, never started
	step(s, window{}, gathered{cat: &catalog{}}, 0)
	if c := s.Current; c != nil && c.ID == id && c.Offers == 0 {
		t.Errorf("%s shelved and reassigned at once: offers reset to 0", id)
	}
}
