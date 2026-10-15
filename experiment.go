package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// Hint policies are experiment arms. Each new problem gets one when it
// starts (balanced); retention is measured by its first cold re-solve.
//
//	ladder:  hint level 1/2/3 unlock after [hints] ladder_min of work; give-up after giveup_min
//	instant: any hint level any time
//	cold:    re-solves and warm-ups; no hints, a give-up just reschedules
//
// Times are work time (Task.Worked), so a suspended laptop unlocks nothing.

// hintAllowance says which hint level the task may get now, and how much
// more work unlocks the next.
func hintAllowance(t *Task) (level int, next time.Duration) {
	if !t.Active() {
		return 0, 0
	}
	switch t.Policy {
	case "instant":
		return 3, 0
	case "ladder":
		el := t.Worked()
		for i, at := range cfg().LadderAt {
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
		return false, cfg().GiveUpAt
	}
	if t.Policy == "ladder" {
		if el := t.Worked(); el < cfg().GiveUpAt {
			return false, cfg().GiveUpAt - el
		}
	}
	return true, 0
}

type armStats struct {
	New, Solo    int
	Probes, Pass int
}

// arms tallies each arm. Retention counts one result per problem: its first
// attempt at the first re-solve, which comes after the same delay for every
// problem. Retries and later stages are practice, not evidence.
func (s *State) arms() map[string]*armStats {
	m := map[string]*armStats{"ladder": {}, "instant": {}}
	counted := map[string]bool{}
	for _, t := range s.History {
		if t.Kind == "new" && !t.Known {
			if a, ok := m[t.Policy]; ok && (t.Outcome == "solo" || t.Outcome == "assisted" || t.Outcome == "gaveup") {
				a.New++
				if t.Outcome == "solo" {
					a.Solo++
				}
			}
		}
		if t.Kind == "resolve" && t.ProbeStage == 1 && t.ProbeTry == 0 && !counted[t.ID] && t.Outcome != "" {
			if a, ok := m[t.Arm]; ok {
				counted[t.ID] = true
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
// The seed is fixed, so the same data always gives the same answer.
func probBetter(passA, nA, passB, nB int) float64 {
	const draws = 20000
	rng := rand.New(rand.NewPCG(1, 2))
	win := 0
	for i := 0; i < draws; i++ {
		if betaSample(rng, 1+float64(passA), 1+float64(nA-passA)) > betaSample(rng, 1+float64(passB), 1+float64(nB-passB)) {
			win++
		}
	}
	return float64(win) / draws
}

func betaSample(rng *rand.Rand, a, b float64) float64 {
	x, y := gammaSample(rng, a), gammaSample(rng, b)
	return x / (x + y)
}

// gammaSample uses Marsaglia–Tsang (shape >= 1, which Beta(1+k, ...) always is).
func gammaSample(rng *rand.Rand, k float64) float64 {
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

// The experiment is decided once, when both arms have this many first
// re-solves: no peeking at a running posterior and stopping on a lucky streak.
const (
	probesPerArm = 30
	winnerConf   = 0.95
)

func (s *State) experimentReport() string {
	m := s.arms()
	l, i := m["ladder"], m["instant"]
	var b strings.Builder
	b.WriteString("Experiment: which hint style makes solutions stick?\n")
	for _, name := range []string{"ladder", "instant"} {
		a := m[name]
		fmt.Fprintf(&b, "  %-8s new %2d (solo %2d)   first re-solves %2d/%d\n", name, a.New, a.Solo, a.Probes, probesPerArm)
	}
	if l.Probes < probesPerArm || i.Probes < probesPerArm {
		fmt.Fprintf(&b, "  Running: the verdict comes once both styles have %d first re-solves.\n", probesPerArm)
		return b.String()
	}
	// Exactly the first probesPerArm of each, so later data can't flip it.
	lp, ip := s.firstPasses("ladder"), s.firstPasses("instant")
	p := probBetter(lp, probesPerArm, ip, probesPerArm)
	fmt.Fprintf(&b, "  retained: ladder %d/%d, instant %d/%d · P(ladder better) = %.0f%%\n", lp, probesPerArm, ip, probesPerArm, 100*p)
	switch {
	case p >= winnerConf:
		b.WriteString("  Verdict: hints that unlock over time work better for you.\n")
	case p <= 1-winnerConf:
		b.WriteString("  Verdict: on-demand hints work better for you. Struggling longer isn't paying off.\n")
	default:
		b.WriteString("  Verdict: no real difference. Use whichever style you like.\n")
	}
	return b.String()
}

// firstPasses counts passes among an arm's first probesPerArm first re-solves.
func (s *State) firstPasses(arm string) int {
	n, pass := 0, 0
	counted := map[string]bool{}
	for _, t := range s.History {
		if t.Kind != "resolve" || t.ProbeStage != 1 || t.ProbeTry != 0 || counted[t.ID] || t.Arm != arm || n >= probesPerArm {
			continue
		}
		counted[t.ID] = true
		n++
		if t.Outcome == "solo" {
			pass++
		}
	}
	return pass
}
