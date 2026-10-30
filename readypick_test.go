package main

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// A faded pattern with a solved problem comes back as a cold re-solve, the
// one nearest P ≈ 0.70 for its θ.
func TestTrial2PicksResolveForFadedPattern(t *testing.T) {
	at(t, t2)
	s := fresh(t)
	var ps []Problem
	for j, r := range []int{900, 1000, 1100, 1200, 1300, 1400} {
		id := fmt.Sprintf("cf:50%c", 'A'+j)
		ps = append(ps, cfp(id, r, "graphs"))
		s.CFDone[id] = t2.AddDate(0, 0, -90+j).Unix() // long ago: memory has faded
	}
	setCat(t, ps...)
	task, err := assign(s, &catalog{cf: ps, err: map[string]error{}})
	if err != nil {
		t.Fatal(err)
	}
	k := s.model().skill["graphs"]
	if task.Kind != "resolve" || task.Policy != "cold" || s.Ready.Pick == nil || s.Ready.Pick.Pattern != "graphs" {
		t.Fatalf("got %+v, pick %+v", task, s.Ready.Pick)
	}
	want := k.Theta - pOffset(pickP)
	for _, p := range ps {
		if math.Abs(float64(p.Rating)-want) < math.Abs(float64(task.Rating)-want) {
			t.Errorf("picked %d, %s at %d is nearer to %.0f", task.Rating, p.ID, p.Rating, want)
		}
	}
	if st := edgeStatus(s); st.Pattern != "Graphs BFS/DFS" {
		t.Errorf("status pattern %q", st.Pattern)
	}
	if s.stats().Readiness.Pick == nil {
		t.Error("stats lack the why-this line")
	}
}

// Nothing faded (no solved problems): a new problem from a Thompson draw,
// LeetCode medium/hard on a weekday, Codeforces on a weekend, near P ≈ 0.70.
func TestTrial2PicksNewBySourceAndDifficulty(t *testing.T) {
	var lc, cf []Problem
	for i := 0; i < 40; i++ {
		lc = append(lc, Problem{ID: fmt.Sprintf("lc:p%d", i), Title: "x", Rating: 1000 + 30*i, Tags: []string{"union-find"}})
		cf = append(cf, Problem{ID: fmt.Sprintf("cf:19%02dA", i), Rating: 800 + 100*(i%10), Tags: []string{"dsu"}, Contest: 1900})
	}
	cat := &catalog{cf: cf, lc: lc, err: map[string]error{}}
	for _, c := range []struct {
		when time.Time
		src  string
	}{{t2, "lc"}, {t2.AddDate(0, 0, 1), "cf"}} {
		at(t, c.when)
		s := fresh(t)
		setCat(t)
		writeConfig(t, "[patterns]\n"+allZeroBut("dsu"))
		task, err := assign(s, cat)
		if err != nil {
			t.Fatal(err)
		}
		if task.Kind != "new" || task.Source() != c.src || s.Ready.Pick.Pattern != "dsu" {
			t.Fatalf("%s: %+v", c.when.Weekday(), task)
		}
		d := float64(cfScale(c.src, task.Rating))
		if want := s.model().skill["dsu"].Theta - pOffset(pickP); math.Abs(d-want) > 100 {
			t.Errorf("%s: difficulty %.0f, want about %.0f", c.src, d, want)
		}
		if c.src == "lc" && task.Rating < lcMediumFloor {
			t.Errorf("weekday LeetCode pick is an easy: %d", task.Rating)
		}
		if task.Focus != map[string]string{"lc": "union-find", "cf": "dsu"}[c.src] {
			t.Errorf("focus %q", task.Focus)
		}
	}
}

func allZeroBut(keep string) string {
	var b string
	for _, p := range patterns {
		if p.ID != keep {
			b += p.ID + " = 0\n"
		}
	}
	return b
}

// Upsolves still come first, and before Trial 2 the picker stays out.
func TestTrial2PickerOrderAndPhase(t *testing.T) {
	cf := []Problem{cfp("cf:1950A", 1000, "dsu")}
	cat := &catalog{cf: cf, err: map[string]error{}}
	at(t, t2)
	s := fresh(t)
	setCat(t)
	missedRound(s, "cf:2272", t2.Add(-4*time.Hour))
	if task, _ := assign(s, cat); task == nil || task.Kind != "upsolve" {
		t.Errorf("trial2 with an upsolve owed: %+v", task)
	}
	at(t, wed)
	s = fresh(t)
	writeConfig(t, "[problems]\nsources = [\"cf\"]\ncf_rating = 1000\n")
	if task, _ := assign(s, cat); task == nil || s.Ready.Pick != nil {
		t.Errorf("baseline used the readiness picker: %+v %+v", task, s.Ready.Pick)
	}
}
