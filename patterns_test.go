package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPatternsFromTags(t *testing.T) {
	for _, c := range []struct {
		src  string
		tags []string
		want []string
	}{
		{"cf", []string{"dp", "greedy", "math"}, []string{"greedy", "dp-1d", "dp-2d"}},
		{"cf", []string{"dfs and similar", "graphs", "trees"}, []string{"graphs", "trees"}},
		{"cf", []string{"math", "implementation"}, nil},
		{"lc", []string{"dynamic-programming", "matrix"}, []string{"dp-2d"}},
		{"lc", []string{"array", "dynamic-programming"}, []string{"dp-1d"}},
		{"lc", []string{"hash-table", "sliding-window", "string"}, []string{"sliding-window", "hashing"}},
		{"lc", []string{"monotonic-stack", "heap-priority-queue"}, []string{"stack", "heap"}},
		{"ac", []string{"dp"}, nil},
	} {
		if got := patternsOf(c.src, c.tags); !slices.Equal(got, c.want) {
			t.Errorf("%s %v: %v, want %v", c.src, c.tags, got, c.want)
		}
	}
	if len(patterns) < 16 {
		t.Errorf("%d patterns", len(patterns))
	}
	for _, m := range []map[string][]string{cfPattern, lcPattern} {
		for tag, ps := range m {
			for _, p := range ps {
				if patternName(p) == "" {
					t.Errorf("%s maps to unknown pattern %s", tag, p)
				}
			}
		}
	}
	if cfScale("lc", 1800) != 1400 || cfScale("cf", 1500) != 1500 || cfScale("ac", 900) != 0 || cfScale("cf", 0) != 0 {
		t.Error("cfScale")
	}
}

// The model reads problems from edge's cache files only, and re-reads
// them when one changes.
func TestCachedProblemsReadsDiskOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDGE_CACHE", dir)
	if m, _ := cachedProblems(); len(m) != 0 {
		t.Fatalf("no files, yet %d problems", len(m))
	}
	write := func(name, body string, at time.Time) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o644)
		os.Chtimes(p, at, at)
	}
	t0 := time.Now().Add(-time.Hour)
	write("cf_problems.json", `{"status":"OK","result":{"problems":[{"contestId":1899,"index":"A","name":"X","rating":800,"tags":["greedy"]}]}}`, t0)
	write("lc_questions.json", `[{"questionFrontendId":"1","title":"Two Sum","titleSlug":"two-sum","isPaidOnly":false,"topicTags":[{"slug":"hash-table"}]}]`, t0)
	write("lc_ratings.txt", "Rating\tID\tTitle\tTitle ZH\tTitle Slug\n1300.5\t1\tTwo Sum\t-\ttwo-sum\n", t0)
	m, _ := cachedProblems()
	if p := m["cf:1899A"]; p.Rating != 800 || !slices.Equal(p.Tags, []string{"greedy"}) {
		t.Errorf("cf: %+v", p)
	}
	if p := m["lc:two-sum"]; p.Rating != 1300 || !slices.Equal(p.Tags, []string{"hash-table"}) {
		t.Errorf("lc: %+v", p)
	}
	write("cf_problems.json", `{"status":"OK","result":{"problems":[{"contestId":1899,"index":"B","name":"Y","rating":900,"tags":[]}]}}`, t0.Add(time.Minute))
	if m, _ := cachedProblems(); m["cf:1899B"].Rating != 900 {
		t.Error("a changed cache file wasn't re-read")
	}
}
