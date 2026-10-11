package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Hint policies are experiment arms. Each new problem gets one at random
// (balanced); retention is measured later by cold re-solves (probes).
//
//	ladder:  hint level 1/2/3 unlock at 20/35/50 min; give-up from 60 min
//	instant: any hint level any time
//	cold:    probes and re-entry; no hints, a give-up just reschedules
var ladderAt = []time.Duration{20 * time.Minute, 35 * time.Minute, 50 * time.Minute}

const ladderGiveup = 60 * time.Minute

// hintAllowance says which hint level the task may get now, and when the next unlocks.
func hintAllowance(t *Task) (level int, next time.Duration) {
	if !t.Active() {
		return 0, 0
	}
	switch t.Policy {
	case "instant":
		return 3, 0
	case "ladder":
		el := now().Sub(t.Started)
		for i, at := range ladderAt {
			if el < at {
				return i, at - el
			}
		}
		return 3, 0
	}
	return 0, 0
}

func canGiveUp(t *Task) (bool, time.Duration) {
	if !t.Active() {
		return false, ladderGiveup
	}
	if t.Policy == "ladder" {
		if el := now().Sub(t.Started); el < ladderGiveup {
			return false, ladderGiveup - el
		}
	}
	return true, 0
}

type armStats struct {
	New, Solo     int
	Probes, Pass  int
	MinutesToDone []float64
}

func (s *State) arms() map[string]*armStats {
	m := map[string]*armStats{"ladder": {}, "instant": {}}
	for _, t := range s.History {
		if t.Kind == "new" {
			if a, ok := m[t.Policy]; ok && t.Outcome != "skipped" {
				a.New++
				if t.Outcome == "solo" {
					a.Solo++
				}
			}
		}
		// A probe's arm is the arm of the original attempt it re-tests.
		if t.Kind == "resolve" && t.Outcome != "skipped" {
			if a, ok := m[t.Arm]; ok {
				a.Probes++
				if t.Outcome == "solo" {
					a.Pass++
				}
			}
		}
	}
	return m
}

// probBetter is P(rate_a > rate_b) under Beta(1+pass, 1+fail) posteriors,
// estimated by sampling.
func probBetter(passA, nA, passB, nB int) float64 {
	const draws = 20000
	win := 0
	for i := 0; i < draws; i++ {
		if betaSample(1+float64(passA), 1+float64(nA-passA)) > betaSample(1+float64(passB), 1+float64(nB-passB)) {
			win++
		}
	}
	return float64(win) / draws
}

func betaSample(a, b float64) float64 {
	x, y := gammaSample(a), gammaSample(b)
	return x / (x + y)
}

// gammaSample uses Marsaglia–Tsang (shape >= 1, which Beta(1+k, ...) always is).
func gammaSample(k float64) float64 {
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if math.Log(u) < 0.5*x*x+d-d*v+d*math.Log(v) {
			return d * v
		}
	}
}

const (
	minProbesPerArm = 10
	winnerConf      = 0.95
)

func (s *State) experimentReport() string {
	m := s.arms()
	l, i := m["ladder"], m["instant"]
	var b strings.Builder
	b.WriteString("Experiment: which hint style makes solutions stick?\n")
	for _, name := range []string{"ladder", "instant"} {
		a := m[name]
		fmt.Fprintf(&b, "  %-8s new %2d (solo %2d)   cold re-solves %2d/%-2d", name, a.New, a.Solo, a.Pass, a.Probes)
		if a.Probes > 0 {
			fmt.Fprintf(&b, " = %3.0f%%", 100*float64(a.Pass)/float64(a.Probes))
		}
		b.WriteString("\n")
	}
	if l.Probes == 0 || i.Probes == 0 {
		b.WriteString("  No retention data yet: it comes from re-solves 3+ days after a problem.\n")
		return b.String()
	}
	p := probBetter(l.Pass, l.Probes, i.Pass, i.Probes)
	fmt.Fprintf(&b, "  P(ladder retains better than instant) = %.0f%%\n", 100*p)
	switch {
	case l.Probes < minProbesPerArm || i.Probes < minProbesPerArm:
		fmt.Fprintf(&b, "  Too early: need %d re-solves per style before calling it.\n", minProbesPerArm)
	case p >= winnerConf:
		b.WriteString("  Verdict: hints that unlock over time work better for you.\n")
	case p <= 1-winnerConf:
		b.WriteString("  Verdict: on-demand hints work better for you. Struggling longer isn't paying off.\n")
	default:
		b.WriteString("  No clear winner yet, so the experiment keeps running.\n")
	}
	return b.String()
}
