package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Trial 2: a rejected verdict asks what it was, in the fail card's place.
func TestMistakeCardAndLog(t *testing.T) {
	actionEnv(t)
	at(t, t2)
	s := fresh(t)
	setCat(t)
	s.Current = &Task{ID: "cf:2000C", Kind: "new", Title: "Trees", Policy: "ladder", Assigned: t2.Add(-time.Hour)}
	begin(s, s.Current, t2.Add(-time.Hour))
	s.Current.Fails, s.Current.Verdict = 1, "WA on test 7"
	evs := readinessEvents(s, window{}, now())
	if len(evs) != 1 || evs[0].Title != "WA on test 7. What was it?" || len(evs[0].Chips) != 5 {
		t.Fatalf("events %+v", evs)
	}
	c := emberCardFor(evs[0])
	if c.ID != "fail" || c.Chips[1].Key != "mistake:cf:2000C:edge" || c.Chips[1].Label != "Edge case" || cardBind(evs[0]) != "task:cf:2000C" {
		t.Errorf("card %+v bind %s", c, cardBind(evs[0]))
	}
	for _, ch := range c.Chips {
		if len(ch.Label) > 18 {
			t.Errorf("chip label too long: %q", ch.Label)
		}
	}
	// an old verdict isn't asked about later
	s.Said["fail:cf:2000C:1"] = now().Add(-11 * time.Minute)
	if evs := readinessEvents(s, window{}, now()); len(evs) != 0 {
		t.Errorf("asked about an old verdict: %+v", evs)
	}
	delete(s.Said, "fail:cf:2000C:1")
	s.save()
	if _, err := act("mistake:cf:2000C:sloppy"); err == nil || strings.Contains(err.Error(), "unknown action") {
		t.Errorf("bad kind: %v", err)
	}
	if _, err := act("mistake:cf:2000C:edge"); err != nil {
		t.Fatal(err)
	}
	at(t, t2.Add(5*time.Second))
	if _, err := act("mistake:cf:2000C:overflow"); err != nil { // changing the answer
		t.Fatal(err)
	}
	s, _ = load()
	if len(s.Ready.Mistakes) != 1 || s.Ready.Mistakes[0].Kind != "overflow" || s.Ready.Mistakes[0].Verdict != "WA on test 7" {
		t.Errorf("mistakes %+v", s.Ready.Mistakes)
	}
	if evs := readinessEvents(s, window{}, now()); len(evs) != 0 {
		t.Errorf("asked again after an answer: %+v", evs)
	}
	s.Ready.Mistakes = append(s.Ready.Mistakes, Mistake{At: t2.AddDate(0, 0, -60), Task: "cf:1A", Fail: 1, Kind: "bug"},
		Mistake{At: t2.AddDate(0, 0, -50), Task: "cf:1B", Fail: 1, Kind: "bug"})
	ms := s.mistakeStats()
	if ms.Total != 3 || ms.All["bug"] != 2 || ms.Recent["bug"] != 0 || ms.Top != "overflow" {
		t.Errorf("stats %+v", ms)
	}
	// baseline: no card
	at(t, wed)
	s.Current.Fails = 2
	if evs := readinessEvents(s, window{}, now()); len(evs) != 0 {
		t.Errorf("baseline asked: %+v", evs)
	}
}

func lcDrillCat(t *testing.T) []Problem {
	var ps []Problem
	for i := 0; i < 30; i++ {
		ps = append(ps, Problem{ID: fmt.Sprintf("lc:q%d", i), Title: fmt.Sprintf("%d. Q", 3000+i), Rating: 1400 + 20*i,
			Tags: []string{"union-find", "graph", "hash-table", "dynamic-programming", "sorting", "stack"}})
	}
	setCat(t, ps...)
	return ps
}

// The weekly drill: offered while the laptop is in use and nothing is
// started; Start makes it the current task, timed, with its prediction.
func TestDrillOfferStartAndBrier(t *testing.T) {
	actionEnv(t)
	at(t, t2)
	s := fresh(t)
	lcDrillCat(t)
	week := weekStart(t2).Format(time.DateOnly)
	s.Ready.DrillWeek, s.Ready.DrillAt = week, t2.Add(-time.Minute)
	s.Current = &Task{ID: "cf:1950A", Kind: "new", Title: "Old", Assigned: t2.Add(-time.Hour)}

	seenWin.at = time.Time{}
	if evs := readinessEvents(s, window{}, now()); len(evs) != 0 {
		t.Fatalf("offered with no sign of use: %+v", evs)
	}
	noteActivity(window{ID: 7, App: "firefox", Title: "mail"}, now().Add(-6*time.Minute))
	if evs := readinessEvents(s, window{ID: 7, App: "firefox", Title: "mail"}, now()); len(evs) != 0 {
		t.Fatal("offered while idle for 6 minutes")
	}
	evs := readinessEvents(s, window{ID: 7, App: "firefox", Title: "news"}, now())
	if len(evs) != 1 || evs[0].Key != "drill:"+week+":1" || evs[0].Chips[0].Key != "drill" || evs[0].Chips[1].Key != "drill:later" {
		t.Fatalf("drill card: %+v", evs)
	}
	s.Said[evs[0].Key] = now()
	s.save()

	if _, err := act("drill:later"); err != nil {
		t.Fatal(err)
	}
	s, _ = load()
	at(t, t2.Add(20*time.Minute))
	noteActivity(window{ID: 8}, now())
	if evs := readinessEvents(s, window{ID: 8}, now()); len(evs) != 0 {
		t.Fatal("asked again before 30 minutes")
	}
	at(t, t2.Add(31*time.Minute))
	noteActivity(window{ID: 9}, now())
	evs = readinessEvents(s, window{ID: 9}, now())
	if len(evs) != 1 || evs[0].Key != "drill:"+week+":2" {
		t.Fatalf("second offer: %+v", evs)
	}
	s.Said[evs[0].Key] = now()
	s.save()

	msg, err := act("drill")
	if err != nil {
		t.Fatal(err)
	}
	s, _ = load()
	d := s.Current
	if d == nil || d.Kind != "drill" || !d.Active() || d.Policy != "cold" || len(s.Ready.Drills) != 1 || !strings.HasPrefix(msg, "Drill") {
		t.Fatalf("drill: %+v %q", d, msg)
	}
	if p := s.Ready.Drills[0].P; p < 0.5 || p > 0.7 || d.Rating < lcMediumFloor {
		t.Errorf("drill P %.2f rating %d", p, d.Rating)
	}
	if len(s.History) != 0 {
		t.Errorf("the unstarted task left a trace: %+v", s.History)
	}
	st := edgeStatus(s)
	if !st.Drill || st.Pattern == "" || st.DeadlineMs == 0 {
		t.Errorf("status %+v", st)
	}
	if evs := readinessEvents(s, window{ID: 10}, now()); len(evs) != 0 {
		t.Errorf("a second drill offered the same week: %+v", evs)
	}

	d.WorkSec = 1800
	accept(s, d, now())
	st2 := s.readinessStats()
	p := s.Ready.Drills[0].P
	if len(st2.Drills) != 1 || st2.Drills[0].Result != "ac" || st2.Brier == nil || !near(*st2.Brier, (p-1)*(p-1), 0.001) {
		t.Errorf("drill stats %+v brier %v", st2.Drills, st2.Brier)
	}
}

func TestDrillRefusals(t *testing.T) {
	actionEnv(t)
	at(t, wed)
	s := fresh(t)
	setCat(t)
	s.save()
	if _, err := act("drill"); err == nil || strings.Contains(err.Error(), "unknown") {
		t.Errorf("baseline drill: %v", err)
	}
	at(t, t2)
	s.Current = &Task{ID: "cf:1950A", Kind: "new", Title: "Old", Assigned: t2.Add(-time.Hour)}
	begin(s, s.Current, t2.Add(-time.Hour))
	s.save()
	if _, err := act("drill"); err == nil {
		t.Error("a drill over a started task")
	}
}

// The moment is somewhere in this week's study time, from today on.
func TestDrillMomentIsInStudyTime(t *testing.T) {
	at(t, t2) // Friday
	s := fresh(t)
	for i := 0; i < 200; i++ {
		s.rollDrill(now())
		a := s.Ready.DrillAt
		if day(a).Before(day(t2)) || day(a).After(day(t2).AddDate(0, 0, 2)) || !studyTime(a) || a.Sub(day(a)) > 22*time.Hour {
			t.Fatalf("drill moment %v", a)
		}
	}
	// a day that passed without the card rolls a later moment
	s.Ready.DrillAt = t2.AddDate(0, 0, -1)
	s.drillEvent(now())
	if day(s.Ready.DrillAt).Before(day(t2)) {
		t.Errorf("not re-rolled: %v", s.Ready.DrillAt)
	}
}
