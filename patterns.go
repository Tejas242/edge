package main

import (
	"os"
	"path/filepath"
	"slices"
	gosync "sync"
	"time"
)

// The interview patterns Trial 2 measures readiness on: the families Google,
// Uber, Databricks and Rubrik onsites draw from. Weights start equal and can
// be set in config.toml's [patterns].
type pattern struct{ ID, Name string }

var patterns = []pattern{
	{"two-pointers", "Two pointers"},
	{"sliding-window", "Sliding window"},
	{"binary-search", "Binary search"},
	{"prefix-sums", "Prefix sums"},
	{"hashing", "Hashing"},
	{"stack", "Stack"},
	{"heap", "Heap"},
	{"intervals", "Intervals & sorting"},
	{"greedy", "Greedy"},
	{"graphs", "Graphs BFS/DFS"},
	{"shortest-paths", "Shortest paths"},
	{"dsu", "Union-find"},
	{"trees", "Trees"},
	{"topo-sort", "Topological sort"},
	{"dp-1d", "DP 1D"},
	{"dp-2d", "DP 2D / knapsack"},
	{"backtracking", "Backtracking"},
}

func patternName(id string) string {
	for _, p := range patterns {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// cfPattern maps Codeforces tags to patterns. Codeforces can't tell 1D from
// 2D DP, so "dp" is evidence for both. Tags with no interview pattern
// (math, constructive, implementation...) map to nothing.
var cfPattern = map[string][]string{
	"two pointers":    {"two-pointers"},
	"binary search":   {"binary-search"},
	"ternary search":  {"binary-search"},
	"hashing":         {"hashing"},
	"sortings":        {"intervals"},
	"greedy":          {"greedy"},
	"graphs":          {"graphs"},
	"dfs and similar": {"graphs"},
	"shortest paths":  {"shortest-paths"},
	"dsu":             {"dsu"},
	"trees":           {"trees"},
	"dp":              {"dp-1d", "dp-2d"},
}

// lcPattern maps LeetCode topic slugs to patterns. DP is 2D when the
// problem is also a matrix or a string problem (grids, LCS, edit distance),
// 1D otherwise (patternsOf).
var lcPattern = map[string][]string{
	"two-pointers":         {"two-pointers"},
	"sliding-window":       {"sliding-window"},
	"binary-search":        {"binary-search"},
	"prefix-sum":           {"prefix-sums"},
	"hash-table":           {"hashing"},
	"rolling-hash":         {"hashing"},
	"stack":                {"stack"},
	"monotonic-stack":      {"stack"},
	"monotonic-queue":      {"stack"},
	"heap-priority-queue":  {"heap"},
	"sorting":              {"intervals"},
	"line-sweep":           {"intervals"},
	"greedy":               {"greedy"},
	"graph":                {"graphs"},
	"depth-first-search":   {"graphs"},
	"breadth-first-search": {"graphs"},
	"shortest-path":        {"shortest-paths"},
	"union-find":           {"dsu"},
	"tree":                 {"trees"},
	"binary-tree":          {"trees"},
	"binary-search-tree":   {"trees"},
	"topological-sort":     {"topo-sort"},
	"backtracking":         {"backtracking"},
}

// patternsOf is the patterns a problem with these tags exercises, in the
// order of the pattern list. src is "cf", "lc" (or "ac": none).
func patternsOf(src string, tags []string) []string {
	on := map[string]bool{}
	switch src {
	case "cf":
		for _, t := range tags {
			for _, p := range cfPattern[t] {
				on[p] = true
			}
		}
	case "lc":
		for _, t := range tags {
			for _, p := range lcPattern[t] {
				on[p] = true
			}
		}
		if slices.Contains(tags, "dynamic-programming") || slices.Contains(tags, "memoization") {
			if slices.Contains(tags, "matrix") || slices.Contains(tags, "string") {
				on["dp-2d"] = true
			} else {
				on["dp-1d"] = true
			}
		}
	}
	var out []string
	for _, p := range patterns {
		if on[p.ID] {
			out = append(out, p.ID)
		}
	}
	return out
}

// hasPattern: a problem with these tags exercises pattern id.
func hasPattern(src string, tags []string, id string) bool {
	return slices.Contains(patternsOf(src, tags), id)
}

// lcOffset puts LeetCode on the Codeforces scale: d_CF = d_LC − 400.
// zerotrac rates LeetCode problems on LeetCode's contest scale, where
// everyone starts at 1500 and the Knight badge (top ~25%) sits near 1850;
// on Codeforces the same top quarter of active users is Specialist,
// 1400–1500. The Guardian line (top ~5%, about 2200) lines up with Expert
// to Candidate Master (1700–1900). Both anchors put the gap at 350–450.
const lcOffset = 400

// cfScale is a problem's difficulty on the Codeforces scale; 0 = unknown.
func cfScale(src string, rating int) int {
	switch {
	case rating <= 0:
		return 0
	case src == "lc":
		return rating - lcOffset
	case src == "cf":
		return rating
	}
	return 0
}

// diskCatalog is the problem lists from edge's cache files alone, never
// the network: the model reads problems' tags and ratings from it, kept in
// memory until a cache file changes. Missing files mean fewer problems.
var (
	diskMu    gosync.Mutex
	diskStamp string
	diskProbs map[string]Problem
)

func cachedProblems() (map[string]Problem, string) {
	dir := cacheDir()
	names := []string{"cf_problems.json", "lc_questions.json", "lc_ratings.txt"}
	stamp := dir
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil {
			stamp += "|" + st.ModTime().Format(time.RFC3339Nano)
		} else {
			stamp += "|-"
		}
	}
	diskMu.Lock()
	defer diskMu.Unlock()
	if diskProbs != nil && stamp == diskStamp {
		return diskProbs, stamp
	}
	m := map[string]Problem{}
	if b, err := os.ReadFile(filepath.Join(dir, names[0])); err == nil {
		if ps, err := parseCF(b); err == nil {
			for _, p := range ps {
				m[p.ID] = p
			}
		}
	}
	if qb, err := os.ReadFile(filepath.Join(dir, names[1])); err == nil {
		if rb, err := os.ReadFile(filepath.Join(dir, names[2])); err == nil {
			if qs, err := parseLCQuestions(qb); err == nil {
				for _, p := range parseLC(qs, rb) {
					m[p.ID] = p
				}
			}
		}
	}
	diskProbs, diskStamp = m, stamp
	return m, stamp
}

// readyCatalog is swapped in tests.
var readyCatalog = cachedProblems
