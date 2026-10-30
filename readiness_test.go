package main

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"
)

// Friday 20 Nov 2026, 21:00: Trial 2's first evening.
var t2 = time.Date(2026, 11, 20, 21, 0, 0, 0, time.Local)

var catN int

// setCat makes the model's problem lists these problems.
func setCat(t *testing.T, ps ...Problem) map[string]Problem {
	t.Helper()
	m := map[string]Problem{}
	for _, p := range ps {
		m[p.ID] = p
	}
	catN++
	stamp := fmt.Sprint("test", catN)
	old := readyCatalog
	readyCatalog = func() (map[string]Problem, string) { return m, stamp }
	t.Cleanup(func() { readyCatalog = old })
	return m
}

func cfp(id string, rating int, tags ...string) Problem {
	return Problem{ID: id, Title: id, URL: "https://codeforces.com/" + id, Rating: rating, Tags: tags, Contest: 1900}
}

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// Seed: θ from the upper quantile of what he solved in the pattern, shrunk
// to his overall level; σ narrows with the count; memory from the dates.
func TestReadySeedFromCodeforcesHistory(t *testing.T) {
	at(t, t2)
	s := fresh(t)
	setCat(t, cfp("cf:1A", 800, "greedy"), cfp("cf:1B", 1000, "greedy"), cfp("cf:1C", 1200, "greedy"),
		cfp("cf:1D", 1400, "greedy"), cfp("cf:1E", 1600, "graphs"))
	for i, id := range []string{"cf:1A", "cf:1B", "cf:1C", "cf:1D", "cf:1E"} {
		s.CFDone[id] = t2.AddDate(0, 0, -30+i*3).Unix()
	}
	sk := s.skills(func() map[string]Problem { m, _ := readyCatalog(); return m }())
	all := quantile([]float64{800, 1000, 1200, 1400, 1600}, seedQ)
	g := sk["greedy"]
	if want := (4*quantile([]float64{800, 1000, 1200, 1400}, seedQ) + 3*all) / 7; !near(g.Theta, want, 0.01) {
		t.Errorf("greedy θ %.1f, want %.1f", g.Theta, want)
	}
	if !near(g.Sigma, 350/math.Sqrt(1+4.0/3), 0.01) || sk["graphs"].Sigma <= g.Sigma {
		t.Errorf("σ greedy %.0f graphs %.0f: more solves, narrower", g.Sigma, sk["graphs"].Sigma)
	}
	if h := sk["heap"]; h.Theta != all || h.Sigma != 350 || !h.Last.IsZero() || h.memory(t2) != 0 {
		t.Errorf("no data: %+v", *h)
	}
	// greedy solved on days -30, -27, -24, -21: three spaced successes
	if !near(g.H, halfLifeStart*2.5*2.5*2.5, 0.01) {
		t.Errorf("greedy half-life %.1f", g.H)
	}
	if r := g.memory(t2); !near(r, math.Pow(2, -21/g.H), 1e-9) {
		t.Errorf("greedy r %.3f", r)
	}
}

// Each finished task moves θ by K·(score − P), and memory by its rules.
func TestReadyUpdatesFromResults(t *testing.T) {
	at(t, t2)
	s := fresh(t)
	setCat(t, cfp("cf:2A", 1500, "dsu"), cfp("cf:2B", 1500, "dsu"), cfp("cf:2C", 1500, "dsu"), cfp("cf:2D", 1500, "dsu"))
	base := s.model().skill["dsu"].Theta
	add := func(id, outcome string, work time.Duration, known bool) {
		end := now().Add(-time.Hour)
		s.History = append(s.History, Task{ID: id, Kind: "new", Outcome: outcome, Started: end.Add(-work), Finished: end,
			WorkSec: int(work.Seconds()), Known: known})
	}
	add("cf:2A", "solo", 30*time.Minute, false)
	up := s.model().skill["dsu"]
	if up.Theta <= base || up.memory(now()) != 1 {
		t.Fatalf("AC in time: θ %.0f → %.0f, r %.2f", base, up.Theta, up.memory(now()))
	}
	th := up.Theta
	add("cf:2B", "solo", 50*time.Minute, false) // late: ½
	late := s.model().skill["dsu"].Theta
	if d := late - th; d <= 0 || d >= up.Theta-base {
		t.Errorf("a late AC moves less: %+.1f vs %+.1f", d, up.Theta-base)
	}
	add("cf:2C", "skipped", 2*time.Minute, false) // a skip before work says nothing
	add("cf:2D", "solo", 3*time.Minute, true)     // seen before: nothing
	if x := s.model().skill["dsu"].Theta; x != late {
		t.Errorf("skip/known moved θ %.1f → %.1f", late, x)
	}
	h := s.model().skill["dsu"].H
	add("cf:2C", "gaveup", time.Hour, false)
	k := s.model().skill["dsu"]
	if k.Theta >= late || k.H != max(halfLifeMin, h/2) {
		t.Errorf("give-up: θ %.0f (was %.0f), h %.1f (was %.1f)", k.Theta, late, k.H, h)
	}
	// a pattern with no edge result never moves
	if g := s.model().skill["greedy"]; g.Theta != base || g.Solved != 0 {
		t.Errorf("greedy moved: %+v", *g)
	}
}

func TestReadyMemoryHalfLife(t *testing.T) {
	at(t, t2)
	k := &skill{H: halfLifeStart}
	k.succeed(t2.AddDate(0, 0, -10))
	if r := k.memory(t2); !near(r, 0.5, 1e-9) {
		t.Errorf("one half-life on: r %.3f", r)
	}
	k.succeed(t2.AddDate(0, 0, -10).Add(time.Hour)) // same evening: not spaced
	if k.H != halfLifeStart {
		t.Errorf("same-day success stretched h to %.1f", k.H)
	}
	k.succeed(t2)
	if k.H != 25 || k.memory(t2) != 1 {
		t.Errorf("spaced success: h %.1f r %.2f", k.H, k.memory(t2))
	}
	if r := k.memory(t2.Add(6 * time.Hour)); r != 1 {
		t.Errorf("r moved within the edge day: %.4f", r) // whole days only: 03:00 is still Friday
	}
	k.fail()
	if k.H != 12.5 {
		t.Errorf("fail: h %.1f", k.H)
	}
}

// Readiness sits inside its interval, is stable for the same inputs (the
// stats don't churn every tick) and moves the next day as memory fades.
func TestReadinessIntervalAndStability(t *testing.T) {
	at(t, t2)
	s := fresh(t)
	var ps []Problem
	for i, tag := range []string{"greedy", "graphs", "dp", "binary search", "two pointers", "dsu", "trees", "sortings"} {
		for j := 0; j < 6; j++ {
			id := fmt.Sprintf("cf:%d%c", 100+i, 'A'+j)
			ps = append(ps, cfp(id, 1000+100*j, tag))
			s.CFDone[id] = t2.AddDate(0, 0, -3*j-1).Unix()
		}
	}
	setCat(t, ps...)
	st := s.readinessStats()
	if st.R <= 0 || st.Lo > st.R || st.R > st.Hi || st.Lo == st.Hi || st.Hi > 1 {
		t.Fatalf("R %.3f in [%.3f, %.3f]", st.R, st.Lo, st.Hi)
	}
	if len(st.Patterns) != len(patterns) || st.Model != readyModelVersion || st.Interview != interviewDefault {
		t.Fatalf("stats: %+v", st)
	}
	for _, p := range st.Patterns {
		if p.Lo > p.Ready || p.Ready > p.Hi || !near(p.Ready, p.P*p.R, 0.002) {
			t.Errorf("%s: ready %.3f in [%.3f, %.3f], p %.3f r %.3f", p.ID, p.Ready, p.Lo, p.Hi, p.P, p.R)
		}
	}
	r0 := s.model().R
	a, _ := json.Marshal(st)
	at(t, t2.Add(2*time.Hour))
	readyMemo.key = "" // even recomputed, the same inputs give the same numbers
	b, _ := json.Marshal(s.readinessStats())
	if string(a) != string(b) {
		t.Error("readiness changed within the day with nothing new")
	}
	at(t, t2.AddDate(0, 0, 3))
	if r := s.model().R; r >= r0 {
		t.Errorf("three days on, memory faded but R %.4f → %.4f", r0, r)
	}
}

func TestReadyConfigWeights(t *testing.T) {
	at(t, t2)
	s := fresh(t)
	setCat(t)
	writeConfig(t, "[patterns]\ngraphs = 3\ndp-2d = 0\nfrobs = 2\n[readiness]\ninterview_rating = 1500\n")
	if patternWeight("graphs") != 3 || patternWeight("dp-2d") != 0 || patternWeight("heap") != 1 || interviewRating() != 1500 {
		t.Errorf("weights: %v, interview %v", cfg().Patterns, interviewRating())
	}
	if errs := configErrs(); len(errs) != 1 {
		t.Errorf("an unknown pattern is reported: %v", errs)
	}
	if st := s.readinessStats(); st.Interview != 1500 {
		t.Errorf("interview %d", st.Interview)
	}
}

// Before Trial 2 the readiness fields stay out of the status: the model
// runs in shadow, stats only.
func TestReadinessStatusOnlyInTrial2(t *testing.T) {
	setCat(t, cfp("cf:3A", 1200, "greedy"))
	at(t, wed)
	s := fresh(t)
	s.CFDone["cf:3A"] = wed.AddDate(0, 0, -1).Unix()
	s.Current = &Task{ID: "cf:3A", Kind: "new", Tags: []string{"greedy"}, Assigned: wed}
	st := edgeStatus(s)
	if st.ReadyPct != 0 || st.ReadyHi != 0 || st.Pattern != "" || st.Drill {
		t.Errorf("baseline status carries readiness: %+v", st)
	}
	if s.stats().Readiness == nil {
		t.Error("the shadow model is missing from stats")
	}
	at(t, t2)
	s.CFDone["cf:3A"] = t2.AddDate(0, 0, -1).Unix()
	st = edgeStatus(s)
	if st.ReadyHi == 0 || st.Pattern != "Greedy" {
		t.Errorf("trial2 status: pct %d [%d,%d] pattern %q", st.ReadyPct, st.ReadyLo, st.ReadyHi, st.Pattern)
	}
}
