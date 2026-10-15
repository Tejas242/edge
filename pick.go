package main

import (
	"math/rand/v2"
	"slices"
	"sort"
	"time"
)

// why keeps the point of a task visible.
var why = map[string]string{
	"graphs":              "Graph modelling is the most common Google/Uber onsite family. airspace's lease waits are a graph.",
	"trees":               "Tree DP/LCA questions show up constantly in Google rounds. Tries and B-trees are trees too.",
	"dfs and similar":     "DFS order and timestamps are the basis of every tree and graph trick.",
	"dsu":                 "DSU is Kruskal, connectivity and offline queries: a 10-line weapon interviewers love.",
	"shortest paths":      "Dijkstra variants are routing at Uber and request paths in distributed systems.",
	"dp":                  "DP is the hardest interview pattern to fake. It's where 1200 becomes 1600.",
	"data structures":     "Segment trees, heaps and ordered sets are what storage engines (LSM, B-tree) are made of.",
	"graph":               "Graph modelling is the most common Google/Uber onsite family.",
	"dynamic-programming": "DP is the hardest interview pattern to fake.",
	"union-find":          "DSU: connectivity, Kruskal, offline queries.",
	"topological-sort":    "Topo sort is build systems, schedulers, and Spark/Databricks DAGs.",
	"shortest-path":       "Dijkstra variants are routing at Uber.",
}

// window is the target rating band: base rating plus an adaptive offset.
func (s *State) window(src string) (lo, hi int) {
	base := cfg().Rating[src]
	if base == 0 {
		base = s.Ratings[src]
	}
	if base == 0 {
		base = map[string]int{"cf": 1200, "lc": 1650}[src]
	}
	base += s.Offset[src]
	lo = base / 100 * 100
	if src == "cf" {
		lo = max(lo, 800)
	}
	return lo, lo + cfg().BandWidth
}

// score is how a new task speaks to difficulty: 1 solo, ½ with one hint, 0
// otherwise. Known (seen before), skipped and ignored tasks say nothing.
func score(t Task) (float64, bool) {
	if t.Kind != "new" || t.Known {
		return 0, false
	}
	switch t.Outcome {
	case "solo":
		return 1, true
	case "assisted":
		if t.Hints <= 1 {
			return 0.5, true
		}
		return 0, true
	case "gaveup":
		return 0, true
	}
	return 0, false
}

// adapt moves the band by 100 when the results since the last move are
// clearly too easy or too hard, then starts a fresh window: one streak moves
// it once, not once per task.
func (s *State) adapt(src string) {
	var sum float64
	var n int
	for _, t := range s.History {
		if t.Source() != src || !t.Finished.After(s.AdaptFrom[src]) {
			continue
		}
		if x, ok := score(t); ok {
			sum += x
			n++
		}
	}
	if n < 6 {
		return
	}
	switch r := sum / float64(n); {
	case r >= 0.7:
		s.Offset[src] += 100
	case r <= 0.3:
		s.Offset[src] -= 100
	default:
		return
	}
	s.Offset[src] = max(-300, min(500, s.Offset[src]))
	s.AdaptFrom[src] = now()
}

// focusCounts tallies solved problems per tag, and recent struggles per focus.
func (s *State) focusCounts(src string, probs []Problem) (solved, struggled map[string]int) {
	byID := map[string]Problem{}
	for _, p := range probs {
		byID[p.ID] = p
	}
	solved, struggled = map[string]int{}, map[string]int{}
	add := func(id string) {
		for _, t := range byID[id].Tags {
			solved[t]++
		}
	}
	if src == "cf" {
		for id := range s.CFDone {
			add(id)
		}
	}
	recent := 0
	for i := len(s.History) - 1; i >= 0; i-- {
		t := s.History[i]
		if t.Source() != src {
			continue
		}
		if t.Outcome == "solo" || t.Outcome == "assisted" {
			add(t.ID)
		}
		if t.Kind == "new" && recent < 20 {
			recent++
			if x, ok := score(t); ok && x < 1 && t.Focus != "" {
				struggled[t.Focus]++
			}
		}
	}
	return solved, struggled
}

// pickFocus chooses a weak tag: weight / (1 + solved/3), raised by recent
// struggles in it, so the biggest holes come up most and nothing is starved.
func pickFocus(focus map[string]float64, solved, struggled map[string]int) string {
	keys := make([]string, 0, len(focus))
	for k := range focus {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var total float64
	w := make([]float64, len(keys))
	for i, k := range keys {
		w[i] = focus[k] / float64(1+solved[k]/3) * (1 + 0.5*float64(struggled[k]))
		total += w[i]
	}
	x := rand.Float64() * total
	for i, k := range keys {
		if x -= w[i]; x < 0 {
			return k
		}
	}
	return keys[len(keys)-1]
}

func (s *State) offered(id string) bool {
	if s.Seen[id] || s.CFTried[id] {
		return true
	}
	if _, ok := s.CFDone[id]; ok {
		return true
	}
	if s.Current != nil && s.Current.ID == id {
		return true
	}
	return slices.ContainsFunc(s.History, func(t Task) bool { return t.ID == id })
}

// pickNew picks an unseen problem in the band carrying the focus tag ("" = any).
func (s *State) pickNew(src string, probs []Problem, focus string) *Problem {
	lo, hi := s.window(src)
	var cands []Problem
	for _, p := range probs {
		if p.Rating < lo || p.Rating > hi || (focus != "" && !slices.Contains(p.Tags, focus)) || s.offered(p.ID) {
			continue
		}
		if src == "cf" && (p.Contest < 1300 || slices.Contains(p.Tags, "*special")) {
			continue // old-style and joke problems
		}
		cands = append(cands, p)
	}
	if len(cands) == 0 {
		return nil
	}
	p := cands[rand.IntN(len(cands))]
	return &p
}

// pickPolicy balances experiment arms: the arm with fewest started new tasks.
// It runs when a task starts, not when it's offered, so skipping an
// unstarted problem can't steer which arm it would have had.
func (s *State) pickPolicy() string {
	n := map[string]int{}
	for _, t := range s.History {
		if t.Kind == "new" && t.Active() {
			n[t.Policy]++
		}
	}
	arms := []string{"ladder", "instant"}
	rand.Shuffle(len(arms), func(i, j int) { arms[i], arms[j] = arms[j], arms[i] })
	if n[arms[1]] < n[arms[0]] {
		return arms[1]
	}
	return arms[0]
}

// gapDays is days since the last finished task (0 if never).
func (s *State) gapDays() int {
	for i := len(s.History) - 1; i >= 0; i-- {
		if t := s.History[i]; !t.Finished.IsZero() && credit(t) > 0 {
			return int(day(now()).Sub(day(t.Finished)).Hours() / 24)
		}
	}
	return 0
}

// pickReentry: after a break, a short re-solve of something already beaten,
// well below the band. Returning should feel easy, not punishing.
func (s *State) pickReentry(probs []Problem) *Problem {
	lo, _ := s.window("cf")
	var cands []Problem
	cutoff := now().AddDate(0, 0, -30).Unix()
	for _, p := range probs {
		if at, ok := s.CFDone[p.ID]; ok && at < cutoff && p.Rating >= 800 && p.Rating <= max(900, lo-200) {
			cands = append(cands, p)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	p := cands[rand.IntN(len(cands))]
	return &p
}

func (s *State) dueProbe() int {
	best := -1
	for i, p := range s.Probes {
		if !p.Due.After(now()) && (best < 0 || p.Due.Before(s.Probes[best].Due)) {
			best = i
		}
	}
	return best
}

// nextSource alternates CF and LC for new problems; weekends lean CF
// (longer sessions), weekdays lean LC (shorter, interview-shaped).
func (s *State) nextSource() string {
	srcs := cfg().Sources
	if len(srcs) == 1 {
		return srcs[0]
	}
	var last string
	for i := len(s.History) - 1; i >= 0; i-- {
		if s.History[i].Kind == "new" {
			last = s.History[i].Source()
			break
		}
	}
	if wd := day(now()).Weekday(); wd == time.Saturday || wd == time.Sunday {
		if last == "cf" && rand.IntN(3) == 0 {
			return "lc"
		}
		return "cf"
	}
	if last == "lc" {
		return "cf"
	}
	return "lc"
}
