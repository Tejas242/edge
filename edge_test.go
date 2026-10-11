package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Wednesday 14 Oct 2026, 21:00 local.
var wed = time.Date(2026, 10, 14, 21, 0, 0, 0, time.Local)

func at(t *testing.T, when time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return when }
	t.Cleanup(func() { now = old })
}

func fresh(t *testing.T) *State {
	t.Setenv("EDGE_HOME", t.TempDir())
	s, err := load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func solved(s *State, id, outcome string, start, end time.Time) {
	s.History = append(s.History, Task{ID: id, Kind: "new", Policy: "ladder", Arm: "ladder", Outcome: outcome, Started: start, Finished: end})
}

func TestGateBlocksUntilDailyMinimum(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	h, _ := os.UserHomeDir()
	if v := gateDecide(s, "fix the bug in kinvault", filepath.Join(h, "Gems/kinvault")); !v.Block {
		t.Fatal("should block before any credit")
	}
	if v := gateDecide(s, "  /edge-next", "/tmp"); v.Block {
		t.Fatal("edge commands must pass")
	}
	if v := gateDecide(s, "why does this WA", filepath.Join(h, "CP/codeforces/2259")); v.Block || !strings.Contains(v.Context, "tutor") {
		t.Fatal("workspace should pass with the tutor contract")
	}
	if v := gateDecide(s, "x", filepath.Join(h, "CPfoo")); !v.Block {
		t.Fatal("prefix match must respect path boundaries")
	}
	solved(s, "cf:1A", "solo", wed.Add(-time.Hour), wed.Add(-10*time.Minute))
	if v := gateDecide(s, "fix the bug", "/tmp"); v.Block {
		t.Fatal("one task meets the weekday minimum")
	}
}

func TestGateWaitsForStartDay(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.GateFrom = day(wed).AddDate(0, 0, 1)
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("install day must not block")
	}
	at(t, wed.AddDate(0, 0, 1))
	if !gateDecide(s, "x", "/tmp").Block {
		t.Fatal("should block from the start day")
	}
}

func TestWeekendNeedsThree(t *testing.T) {
	sat := time.Date(2026, 10, 17, 18, 0, 0, 0, time.Local)
	at(t, sat)
	s := fresh(t)
	for i := 0; i < 2; i++ {
		solved(s, "cf:1A", "assisted", sat.Add(-3*time.Hour), sat.Add(-time.Duration(i+1)*time.Hour))
	}
	if !gateDecide(s, "x", "/tmp").Block {
		t.Fatal("2/3 on Saturday should still block")
	}
	solved(s, "cf:2A", "solo", sat.Add(-time.Hour), sat)
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("3/3 should open")
	}
}

func TestHonestGiveupEarnsCredit(t *testing.T) {
	quick := Task{Outcome: "gaveup", Started: wed, Finished: wed.Add(20 * time.Minute)}
	long := Task{Outcome: "gaveup", Started: wed, Finished: wed.Add(50 * time.Minute)}
	if credit(quick) != 0 || credit(long) != 1 || credit(Task{Outcome: "skipped"}) != 0 {
		t.Fatal("credit rules wrong")
	}
}

func TestPassOncePerWeek(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	if err := pass(s); err != nil {
		t.Fatal(err)
	}
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("pass should open today")
	}
	at(t, wed.AddDate(0, 0, 1))
	if err := pass(s); err == nil {
		t.Fatal("second pass in a week must fail")
	}
	at(t, wed.AddDate(0, 0, 5)) // next Monday
	if err := pass(s); err != nil {
		t.Fatal("new week, new pass:", err)
	}
}

func TestHintLadder(t *testing.T) {
	task := &Task{Policy: "ladder", Started: wed}
	for _, c := range []struct {
		min, level int
	}{{0, 0}, {19, 0}, {20, 1}, {36, 2}, {51, 3}} {
		at(t, wed.Add(time.Duration(c.min)*time.Minute))
		if l, _ := hintAllowance(task); l != c.level {
			t.Fatalf("at %dm got level %d want %d", c.min, l, c.level)
		}
	}
	if l, _ := hintAllowance(&Task{Policy: "cold"}); l != 0 {
		t.Fatal("cold re-solves get no hints")
	}
	at(t, wed.Add(30*time.Minute))
	if ok, _ := canGiveUp(task); ok {
		t.Fatal("ladder give-up should wait until 60m")
	}
}

func TestProbeScheduling(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	finish(s, &Task{ID: "cf:1A", Kind: "new", Policy: "instant", Arm: "instant", Outcome: "assisted", Started: wed}, wed)
	finish(s, &Task{ID: "cf:2A", Kind: "new", Policy: "ladder", Arm: "ladder", Outcome: "solo", Started: wed}, wed)
	if len(s.Probes) != 2 {
		t.Fatalf("want 2 probes, got %d", len(s.Probes))
	}
	if d := s.Probes[0].Due.Sub(day(wed)); d != 3*24*time.Hour || s.Probes[0].Policy != "instant" {
		t.Fatalf("assisted → +3d probe tagged with its arm, got %v %s", d, s.Probes[0].Policy)
	}
	if d := s.Probes[1].Due.Sub(day(wed)); d != 14*24*time.Hour {
		t.Fatalf("solo → +14d probe, got %v", d)
	}
	// Passing stage 1 schedules stage 2; failing repeats stage 1.
	s.Probes = nil
	finish(s, &Task{ID: "cf:1A", Kind: "resolve", Policy: "cold", Arm: "instant", ProbeStage: 1, Outcome: "solo", Started: wed}, wed)
	if len(s.Probes) != 1 || s.Probes[0].Stage != 2 || s.Probes[0].Policy != "instant" {
		t.Fatalf("stage 2 not scheduled: %+v", s.Probes)
	}
	s.Probes = nil
	finish(s, &Task{ID: "cf:1A", Kind: "resolve", Policy: "cold", Arm: "instant", ProbeStage: 2, Outcome: "gaveup", Started: wed}, wed)
	if len(s.Probes) != 1 || s.Probes[0].Stage != 2 {
		t.Fatalf("failed probe should repeat its stage: %+v", s.Probes)
	}
	if a := s.arms()["instant"]; a.Probes != 2 || a.Pass != 1 {
		t.Fatalf("probe results must count toward the original arm: %+v", a)
	}
}

func TestExperimentNeedsEvidence(t *testing.T) {
	if p := probBetter(9, 10, 3, 10); p < 0.97 {
		t.Fatalf("9/10 vs 3/10 should be clear, got %.2f", p)
	}
	if p := probBetter(6, 10, 5, 10); p > 0.8 || p < 0.4 {
		t.Fatalf("6/10 vs 5/10 should be a coin flip, got %.2f", p)
	}
	s := &State{}
	for i := 0; i < 15; i++ {
		out := "solo"
		if i%2 == 0 {
			out = "gaveup"
		}
		s.History = append(s.History,
			Task{Kind: "resolve", Arm: "ladder", Outcome: "solo"},
			Task{Kind: "resolve", Arm: "instant", Outcome: out})
	}
	if r := s.experimentReport(); !strings.Contains(r, "unlock over time work better") {
		t.Fatalf("15/15 vs 7/15 should produce a verdict:\n%s", r)
	}
	s.History = s.History[:8]
	if r := s.experimentReport(); !strings.Contains(r, "Too early") {
		t.Fatalf("4 per arm must not produce a verdict:\n%s", r)
	}
}

func TestPickNewRespectsBandTagAndHistory(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.Ratings["cf"] = 1208
	s.CFTried["cf:1900B"] = true
	probs := []Problem{
		{ID: "cf:1900A", Rating: 1300, Tags: []string{"graphs"}, Contest: 1900},
		{ID: "cf:1900B", Rating: 1300, Tags: []string{"graphs"}, Contest: 1900}, // tried before
		{ID: "cf:1900C", Rating: 1700, Tags: []string{"graphs"}, Contest: 1900}, // above band
		{ID: "cf:1900D", Rating: 1300, Tags: []string{"math"}, Contest: 1900},   // wrong tag
		{ID: "cf:500A", Rating: 1300, Tags: []string{"graphs"}, Contest: 500},   // too old
	}
	for i := 0; i < 50; i++ {
		if p := s.pickNew("cf", probs, "graphs"); p == nil || p.ID != "cf:1900A" {
			t.Fatalf("got %+v", p)
		}
	}
	if lo, hi := s.window("cf"); lo != 1200 || hi != 1500 {
		t.Fatalf("band %d-%d", lo, hi)
	}
}

func TestAdaptMovesBand(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	for i := 0; i < 6; i++ {
		s.History = append(s.History, Task{ID: "cf:1A", Kind: "new", Outcome: "solo"})
	}
	s.adapt("cf")
	if s.Offset["cf"] != 100 {
		t.Fatalf("all solo should raise the band, offset %d", s.Offset["cf"])
	}
	for i := 0; i < 8; i++ {
		s.History = append(s.History, Task{ID: "cf:1A", Kind: "new", Outcome: "gaveup"})
	}
	s.adapt("cf")
	if s.Offset["cf"] != 0 {
		t.Fatalf("all failures should lower it, offset %d", s.Offset["cf"])
	}
}

func TestStreak(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	for d := 1; d <= 3; d++ { // Tue, Mon, Sun(needs 3)
		end := wed.AddDate(0, 0, -d)
		solved(s, "cf:1A", "solo", end.Add(-time.Hour), end)
	}
	if n := s.streak(); n != 2 {
		t.Fatalf("Tue+Mon met, Sun didn't (1/3): streak %d", n)
	}
}

func TestStatePersists(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.Current = &Task{ID: "lc:two-sum", Started: wed}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	s2, err := load()
	if err != nil || s2.Current == nil || s2.Current.ID != "lc:two-sum" {
		t.Fatalf("round trip failed: %v %+v", err, s2.Current)
	}
}

// EDGE_LIVE=1 go test -run Live hits the real LeetCode API.
func TestLiveLeetCode(t *testing.T) {
	if os.Getenv("EDGE_LIVE") == "" {
		t.Skip("set EDGE_LIVE=1")
	}
	t.Setenv("EDGE_HOME", t.TempDir())
	t.Setenv("EDGE_CACHE", os.Getenv("EDGE_LIVE_CACHE"))
	s, _ := load()
	s.Ratings["lc"] = 1692
	probs, err := lcProblems()
	if err != nil || len(probs) < 2000 {
		t.Fatalf("lc problems: %d %v", len(probs), err)
	}
	task := &Task{}
	s.History = []Task{{ID: "cf:1A", Kind: "new"}} // last was CF → weekday picks LC
	at(t, wed)
	if err := s.newTask(task, nil); err != nil || task.Source() != "lc" {
		t.Fatalf("%v %+v", err, task)
	}
	t.Logf("picked %s (%d) focus %s tags %v", task.Title, task.Rating, task.Focus, task.Tags)
	starter, err := lcStarter(strings.TrimPrefix(task.ID, "lc:"))
	if err != nil || !strings.Contains(starter, "class Solution") {
		t.Fatalf("starter: %v %q", err, starter)
	}
	t.Log(starter)
}

func TestGateToolHoldsWhileClosed(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	h, _ := os.UserHomeDir()
	cp := filepath.Join(h, "CP", "codeforces")
	gems := filepath.Join(h, "Gems", "kinvault")
	cases := []struct {
		tool  string
		input map[string]any
		cwd   string
		deny  bool
	}{
		{"Read", map[string]any{"file_path": filepath.Join(gems, "main.go")}, cp, true},
		{"Read", map[string]any{"file_path": filepath.Join(cp, "2257/C.cpp")}, cp, false},
		{"Edit", map[string]any{"file_path": filepath.Join(h, ".claude/settings.json")}, cp, true},
		{"Grep", map[string]any{"pattern": "x"}, gems, true},
		{"Grep", map[string]any{"pattern": "x", "path": "../.."}, cp, true},
		{"Bash", map[string]any{"command": "g++ -std=c++20 -O2 -I ~/CP/lib 2257/C.cpp -o 2257/C && ./2257/C < 2257/.testcases/C_in_0.txt"}, cp, false},
		{"Bash", map[string]any{"command": "cd ~/Gems/kinvault && go test ./..."}, cp, true},
		{"Bash", map[string]any{"command": "cat 2257/../../../Gems/kinvault/main.go"}, cp, true},
		{"Bash", map[string]any{"command": "git --git-dir=/home/x/Gems/kinvault/.git log"}, cp, true},
		{"Bash", map[string]any{"command": "cd; ls Gems"}, cp, true},
		{"Bash", map[string]any{"command": "go test ./..."}, gems, true},
		{"Bash", map[string]any{"command": "edge status"}, gems, false},
		{"Bash", map[string]any{"command": "edge status; rm -rf ~/Gems"}, cp, true},
		{"Bash", map[string]any{"command": "echo $SECRET_DIR/x"}, cp, true},
		{"Bash", map[string]any{"command": "curl -s https://codeforces.com/api/user.info?handles=screenager"}, cp, false},
		{"mcp__claude_ai_Vercel__create_deployment", map[string]any{}, cp, true},
		{"WebFetch", map[string]any{"url": "https://cp-algorithms.com"}, cp, false},
	}
	for _, c := range cases {
		if deny, _ := gateTool(s, c.tool, c.input, c.cwd); deny != c.deny {
			t.Errorf("%s %v in %s: deny=%v want %v", c.tool, c.input, c.cwd, deny, c.deny)
		}
	}
	solved(s, "cf:1A", "solo", wed.Add(-time.Hour), wed)
	if deny, _ := gateTool(s, "Bash", map[string]any{"command": "cd ~/Gems/kinvault && go test ./..."}, cp); deny {
		t.Error("quota met: everything opens")
	}
}

func TestGatePromptPracticeOnlyWhileClosed(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	h, _ := os.UserHomeDir()
	if v := gateDecide(s, "/edge-status and also refactor kinvault", filepath.Join(h, "Gems")); v.Block || !strings.Contains(v.Context, "GATE CLOSED") {
		t.Fatalf("edge prompts pass but carry the practice-only contract: %+v", v)
	}
	if v := gateDecide(s, "help with kinvault", filepath.Join(h, "CP")); v.Block || !strings.Contains(v.Context, "GATE CLOSED") {
		t.Fatalf("~/CP passes with the practice-only contract: %+v", v)
	}
	solved(s, "cf:1A", "solo", wed.Add(-time.Hour), wed)
	if v := gateDecide(s, "help with kinvault", filepath.Join(h, "CP")); strings.Contains(v.Context, "GATE CLOSED") {
		t.Fatal("open gate: no practice-only contract")
	}
}
