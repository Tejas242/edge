package main

import (
	"math/rand/v2"
	"slices"
	"sort"
	"time"
)

var rng = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 7))

// Weak areas, from the screenager history: graphs 5, trees 2, dsu 1,
// shortest paths 0, dp 25. Weight is how much interview mileage the tag has.
var focusCF = map[string]float64{
	"graphs": 3, "trees": 3, "dfs and similar": 2, "dsu": 2, "shortest paths": 2,
	"dp": 3, "data structures": 2, "binary search": 1, "two pointers": 1, "bitmasks": 1,
}

var focusLC = map[string]float64{
	"graph": 3, "tree": 2, "binary-tree": 2, "depth-first-search": 2, "breadth-first-search": 2,
	"union-find": 2, "shortest-path": 2, "topological-sort": 2, "dynamic-programming": 3,
	"segment-tree": 1, "monotonic-stack": 1, "sliding-window": 1, "heap-priority-queue": 1,
}

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
	base := s.Ratings[src]
	if base == 0 {
		base = map[string]int{"cf": 1200, "lc": 1650}[src]
	}
	base += s.Offset[src]
	lo = base / 100 * 100
	if src == "cf" {
		lo = max(lo, 800)
	}
	return lo, lo + 300
}

// adapt moves the band: mostly solving alone means too easy, mostly failing
// means too hard. Looks at the last 8 new tasks of that source.
func (s *State) adapt(src string) {
	var solo, n int
	for i := len(s.History) - 1; i >= 0 && n < 8; i-- {
		t := s.History[i]
		if t.Kind != "new" || t.Source() != src || t.Outcome == "skipped" {
			continue
		}
		n++
		if t.Outcome == "solo" {
			solo++
		}
	}
	if n < 4 {
		return
	}
	switch r := float64(solo) / float64(n); {
	case r >= 0.7:
		s.Offset[src] += 100
	case r <= 0.3:
		s.Offset[src] -= 100
	}
	s.Offset[src] = max(-300, min(500, s.Offset[src]))
}

// focusCounts tallies how much of each focus tag has been solved.
func (s *State) focusCounts(src string, probs []Problem) map[string]int {
	byID := map[string]Problem{}
	for _, p := range probs {
		byID[p.ID] = p
	}
	c := map[string]int{}
	add := func(id string) {
		for _, t := range byID[id].Tags {
			c[t]++
		}
	}
	if src == "cf" {
		for id := range s.CFDone {
			add(id)
		}
	}
	for _, t := range s.History {
		if t.Source() == src && (t.Outcome == "solo" || t.Outcome == "assisted") {
			add(t.ID)
		}
	}
	return c
}

// pickFocus chooses a weak tag: weight / (1 + solved in it), so the biggest
// holes come up most, but nothing is starved.
func pickFocus(focus map[string]float64, solved map[string]int) string {
	keys := make([]string, 0, len(focus))
	for k := range focus {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var total float64
	w := make([]float64, len(keys))
	for i, k := range keys {
		w[i] = focus[k] / float64(1+solved[k]/3)
		total += w[i]
	}
	x := rng.Float64() * total
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
	return slices.ContainsFunc(s.History, func(t Task) bool { return t.ID == id })
}

// pickNew picks an unseen problem in the band carrying the focus tag.
func (s *State) pickNew(src string, probs []Problem, focus string) *Problem {
	lo, hi := s.window(src)
	var cands []Problem
	for _, p := range probs {
		if p.Rating < lo || p.Rating > hi || !slices.Contains(p.Tags, focus) || s.offered(p.ID) {
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
	p := cands[rng.IntN(len(cands))]
	return &p
}

// pickPolicy balances experiment arms: the arm with fewest new tasks so far.
func (s *State) pickPolicy() string {
	n := map[string]int{}
	for _, t := range s.History {
		if t.Kind == "new" {
			n[t.Policy]++
		}
	}
	arms := []string{"ladder", "instant"}
	rng.Shuffle(len(arms), func(i, j int) { arms[i], arms[j] = arms[j], arms[i] })
	if n[arms[1]] < n[arms[0]] {
		return arms[1]
	}
	return arms[0]
}

// gapDays is days since the last finished task (0 if never).
func (s *State) gapDays() int {
	for i := len(s.History) - 1; i >= 0; i-- {
		if !s.History[i].Finished.IsZero() && s.History[i].Outcome != "skipped" {
			return int(now().Sub(s.History[i].Finished).Hours() / 24)
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
	p := cands[rng.IntN(len(cands))]
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
	var last string
	for i := len(s.History) - 1; i >= 0; i-- {
		if s.History[i].Kind == "new" {
			last = s.History[i].Source()
			break
		}
	}
	if wd := now().Weekday(); wd == time.Saturday || wd == time.Sunday {
		if last == "cf" && rng.IntN(3) == 0 {
			return "lc"
		}
		return "cf"
	}
	if last == "lc" {
		return "cf"
	}
	return "lc"
}
