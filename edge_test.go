package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain points HOME and every edge dir at a scratch dir, so no test can
// touch the real ~/CP, state or Claude settings.
func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("/var/tmp", "edge-test") // not /tmp: the gate allows /tmp
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{"HOME": d, "EDGE_HOME": d + "/data", "EDGE_WORK": d + "/CP", "EDGE_CACHE": d + "/cache", "XDG_CONFIG_HOME": d + "/config"} {
		os.Setenv(k, v)
	}
	os.MkdirAll(d+"/CP", 0o755)
	code := m.Run()
	os.RemoveAll(d)
	os.Exit(code)
}

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
	t.Setenv("EDGE_CACHE", t.TempDir())
	resetConfig()
	t.Cleanup(resetConfig)
	s, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s.GateFrom = time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local) // installed a while ago
	return s
}

// writeConfig writes config.toml for this test.
func writeConfig(t *testing.T, text string) {
	t.Helper()
	p := configPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	resetConfig()
	t.Cleanup(func() { os.Remove(p); resetConfig() })
}

func solved(s *State, id, outcome string, start, end time.Time) {
	s.History = append(s.History, Task{ID: id, Kind: "new", Policy: "ladder", Arm: "ladder", Outcome: outcome, Started: start, Finished: end,
		WorkSec: int(end.Sub(start).Seconds())})
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

func TestGateOnlyInStudyTime(t *testing.T) {
	s := fresh(t)
	at(t, time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local)) // Wednesday morning: no work on offer
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("the gate must not close before work is offered")
	}
	at(t, time.Date(2026, 10, 15, 1, 30, 0, 0, time.Local)) // still Wednesday evening's session
	if !gateDecide(s, "x", "/tmp").Block {
		t.Fatal("01:30 belongs to Wednesday's session, which isn't done")
	}
	solved(s, "cf:1A", "solo", now().Add(-time.Hour), now().Add(-time.Minute)) // finished 01:29
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("work after midnight counts for the evening it belongs to")
	}
	at(t, time.Date(2026, 10, 15, 4, 30, 0, 0, time.Local))
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("after day_ends a new day starts, and it's not study time yet")
	}
}

func TestGateFailsOpenWhenEdgeCantOfferWork(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.trouble("assign", "no problems")
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("no problem can be assigned: the gate must open")
	}
	s.fixed("assign")
	s.Current = &Task{ID: "cf:1A", Kind: "new", Assigned: wed}
	s.trouble("cf", "codeforces down")
	if !gateDecide(s, "x", "/tmp").Block {
		t.Fatal("a short judge blip keeps the gate")
	}
	at(t, wed.Add(31*time.Minute))
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("judge down 30+ min: no way to earn the day, so open")
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
	s.GateFrom = time.Time{}
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("a fresh state (never installed) must not block")
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
	fresh(t)
	idle := Task{Outcome: "gaveup", Started: wed, Finished: wed.Add(24 * time.Hour), WorkSec: 5 * 60, Edited: true}
	short := Task{Outcome: "gaveup", Started: wed, Finished: wed.Add(50 * time.Minute), WorkSec: 20 * 60, Edited: true}
	blank := Task{Outcome: "gaveup", Started: wed, Finished: wed.Add(50 * time.Minute), WorkSec: 50 * 60}
	real := Task{Outcome: "gaveup", Started: wed, Finished: wed.Add(50 * time.Minute), WorkSec: 50 * 60, Fails: 1}
	if credit(idle) != 0 || credit(short) != 0 || credit(blank) != 0 || credit(real) != 1 || credit(Task{Outcome: "skipped"}) != 0 {
		t.Fatal("give-up credit needs 45 minutes of work and a trace of it")
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

func TestPassRefusesWithoutTerminal(t *testing.T) {
	if passAllowed() == nil {
		t.Fatal("go test has no terminal on stdin/stdout here, and Claude never does")
	}
}

func TestHintLadderCountsWorkNotWallClock(t *testing.T) {
	fresh(t)
	at(t, wed.Add(10*time.Hour)) // hours later, e.g. after a suspend
	task := &Task{Policy: "ladder", Started: wed}
	for _, c := range []struct {
		min, level int
	}{{0, 0}, {19, 0}, {20, 1}, {36, 2}, {51, 3}} {
		task.WorkSec = c.min * 60
		if l, _ := hintAllowance(task); l != c.level {
			t.Fatalf("at %dm of work got level %d want %d", c.min, l, c.level)
		}
	}
	if l, _ := hintAllowance(&Task{Policy: "cold", Started: wed}); l != 0 {
		t.Fatal("cold re-solves get no hints")
	}
	task.WorkSec = 30 * 60
	if ok, _ := canGiveUp(task); ok {
		t.Fatal("ladder give-up should wait until 60m of work")
	}
}

func TestStuckIsIdempotent(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.Current = &Task{ID: "cf:1A", Kind: "new", Policy: "instant", Started: wed, Assigned: wed}
	out = io.Discard
	t.Cleanup(func() { out = os.Stdout })
	if err := stuck(s, 0, ""); err != nil || s.Current.Hints != 0 {
		t.Fatal("looking must not spend a hint")
	}
	for i := 0; i < 3; i++ {
		if err := stuck(s, 1, ""); err != nil || s.Current.Hints != 1 {
			t.Fatalf("taking level 1 again must not spend more: %v %d", err, s.Current.Hints)
		}
	}
	if err := stuck(s, 3, ""); err == nil {
		t.Fatal("levels can't be skipped")
	}
}

func TestProbeScheduling(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	out = io.Discard
	t.Cleanup(func() { out = os.Stdout })
	finish(s, &Task{ID: "cf:1A", Kind: "new", Policy: "instant", Arm: "instant", Outcome: "assisted", Started: wed}, wed)
	finish(s, &Task{ID: "cf:2A", Kind: "new", Policy: "ladder", Arm: "ladder", Outcome: "solo", Started: wed}, wed)
	finish(s, &Task{ID: "cf:3A", Kind: "new", Policy: "ladder", Arm: "ladder", Outcome: "solo", Known: true, Started: wed}, wed)
	if len(s.Probes) != 2 {
		t.Fatalf("want 2 probes (none for a known problem), got %d", len(s.Probes))
	}
	for _, p := range s.Probes {
		if d := p.Due.Sub(day(wed)); d != 5*24*time.Hour || p.Stage != 1 {
			t.Fatalf("every new problem's first re-solve is +5d, whatever the outcome: %+v", p)
		}
	}
	if s.Probes[0].Policy != "instant" {
		t.Fatal("probe tagged with its arm")
	}
	s.Probes = nil
	finish(s, &Task{ID: "cf:1A", Kind: "resolve", Policy: "cold", Arm: "instant", ProbeStage: 1, Outcome: "solo", Started: wed}, wed)
	if len(s.Probes) != 1 || s.Probes[0].Stage != 2 || s.Probes[0].Due.Sub(day(wed)) != 14*24*time.Hour {
		t.Fatalf("stage 2 at +14d: %+v", s.Probes)
	}
	s.Probes = nil
	finish(s, &Task{ID: "cf:1A", Kind: "resolve", Policy: "cold", Arm: "instant", ProbeStage: 2, Outcome: "solo", Started: wed}, wed)
	if len(s.Probes) != 0 {
		t.Fatal("passing stage 2 graduates it")
	}
	finish(s, &Task{ID: "cf:1A", Kind: "resolve", Policy: "cold", Arm: "instant", ProbeStage: 2, Outcome: "gaveup", Started: wed}, wed)
	if len(s.Probes) != 1 || s.Probes[0].Stage != 2 || s.Probes[0].Try != 1 {
		t.Fatalf("failed probe repeats its stage: %+v", s.Probes)
	}
	s.Probes = nil
	finish(s, &Task{ID: "cf:1A", Kind: "resolve", Policy: "cold", Arm: "instant", ProbeStage: 2, ProbeTry: 2, Outcome: "gaveup", Started: wed}, wed)
	if len(s.Probes) != 0 {
		t.Fatal("the third failed try drops it")
	}
	if a := s.arms()["instant"]; a.Probes != 1 || a.Pass != 1 {
		t.Fatalf("only first re-solves count toward the arm: %+v", a)
	}
}

func TestExperimentDecidesOnceAtFixedSize(t *testing.T) {
	if p := probBetter(9, 10, 3, 10); p < 0.97 {
		t.Fatalf("9/10 vs 3/10 should be clear, got %.2f", p)
	}
	if p := probBetter(6, 10, 5, 10); p > 0.8 || p < 0.4 {
		t.Fatalf("6/10 vs 5/10 should be a coin flip, got %.2f", p)
	}
	if probBetter(20, 30, 15, 30) != probBetter(20, 30, 15, 30) {
		t.Fatal("same data, same answer")
	}
	s := &State{}
	add := func(n int) {
		for i := 0; i < n; i++ {
			out := "solo"
			if i%2 == 0 {
				out = "gaveup"
			}
			id := fmt.Sprintf("cf:%dA", len(s.History))
			s.History = append(s.History,
				Task{ID: id, Kind: "resolve", ProbeStage: 1, Arm: "ladder", Outcome: "solo"},
				Task{ID: id + "x", Kind: "resolve", ProbeStage: 1, Arm: "instant", Outcome: out})
		}
	}
	add(10)
	if r := s.experimentReport(); !strings.Contains(r, "Running") || strings.Contains(r, "Verdict") {
		t.Fatalf("10 per arm must not produce a verdict, however lopsided:\n%s", r)
	}
	add(20)
	if r := s.experimentReport(); !strings.Contains(r, "unlock over time work better") {
		t.Fatalf("30/30 vs 15/30 should produce a verdict:\n%s", r)
	}
	// repeats of the same problem are practice, not evidence
	s2 := &State{}
	for i := 0; i < 40; i++ {
		s2.History = append(s2.History, Task{ID: "cf:1A", Kind: "resolve", ProbeStage: 1, Arm: "ladder", Outcome: "solo"})
	}
	if a := s2.arms()["ladder"]; a.Probes != 1 {
		t.Fatalf("one result per problem, got %d", a.Probes)
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
	seenMath := false
	for i := 0; i < 50; i++ {
		if p := s.pickNew("cf", probs, ""); p != nil && p.ID == "cf:1900D" {
			seenMath = true
		}
	}
	if !seenMath {
		t.Fatal("any-topic picks must reach outside the focus list")
	}
	if lo, hi := s.window("cf"); lo != 1200 || hi != 1500 {
		t.Fatalf("band %d-%d", lo, hi)
	}
}

func TestAdaptMovesBandOncePerWindow(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	for i := 0; i < 5; i++ {
		s.History = append(s.History, Task{ID: "cf:1A", Kind: "new", Outcome: "solo", Finished: wed})
		s.adapt("cf")
	}
	if s.Offset["cf"] != 0 {
		t.Fatal("too few results to move")
	}
	s.History = append(s.History, Task{ID: "cf:1A", Kind: "new", Outcome: "solo", Finished: wed})
	s.adapt("cf")
	if s.Offset["cf"] != 100 {
		t.Fatalf("6 solos raise the band, offset %d", s.Offset["cf"])
	}
	at(t, wed.Add(time.Hour))
	for i := 0; i < 5; i++ {
		s.History = append(s.History, Task{ID: "cf:1A", Kind: "new", Outcome: "solo", Finished: now()})
		s.adapt("cf")
	}
	if s.Offset["cf"] != 100 {
		t.Fatalf("a streak moves the band once per window, not once per task: %d", s.Offset["cf"])
	}
	for i := 0; i < 6; i++ {
		s.History = append(s.History, Task{ID: "cf:1A", Kind: "new", Outcome: "solo", Known: true, Finished: now()})
		s.adapt("cf")
	}
	if s.Offset["cf"] != 100 {
		t.Fatal("known problems say nothing about difficulty")
	}
}

func TestRatingRiseEatsOffset(t *testing.T) {
	s := fresh(t)
	s.Ratings["cf"], s.Offset["cf"] = 1200, 200
	s.setRating("cf", 1350)
	if s.Offset["cf"] != 50 {
		t.Fatalf("progress counted twice: offset %d", s.Offset["cf"])
	}
	s.setRating("cf", 1500)
	if s.Offset["cf"] != 0 {
		t.Fatalf("offset %d", s.Offset["cf"])
	}
}

func TestKnownProblemDetected(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	out = io.Discard
	t.Cleanup(func() { out = os.Stdout })
	task := &Task{ID: "lc:x", Kind: "new", Policy: "instant", Arm: "instant", Started: wed.Add(-3 * time.Minute), WorkSec: 180}
	s.Current = task
	accept(s, task, wed)
	if h := s.History[0]; !h.Known || !s.Seen["lc:x"] || len(s.Probes) != 0 || credit(h) != 1 {
		t.Fatalf("3-minute solve of a new problem: %+v probes %v", h, s.Probes)
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
	s.Passes = append(s.Passes, wed.AddDate(0, 0, -3)) // Sunday
	for i := 0; i < 3; i++ {                           // Saturday 3/3
		end := wed.AddDate(0, 0, -4).Add(-time.Duration(i) * time.Hour)
		solved(s, "cf:1A", "solo", end.Add(-time.Hour), end)
	}
	if n := s.streak(); n != 3 {
		t.Fatalf("a pass day bridges but doesn't count: Tue Mon (Sun pass) Sat = 3, got %d", n)
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
	s := fresh(t)
	t.Setenv("EDGE_CACHE", os.Getenv("EDGE_LIVE_CACHE"))
	s.Ratings["lc"] = 1692
	probs, err := lcProblems()
	if err != nil || len(probs) < 2000 {
		t.Fatalf("lc problems: %d %v", len(probs), err)
	}
	task := &Task{}
	s.History = []Task{{ID: "cf:1A", Kind: "new"}} // last was CF → weekday picks LC
	at(t, wed)
	if err := s.newTask(task, &catalog{lc: probs}); err != nil || task.Source() != "lc" {
		t.Fatalf("%v %+v", err, task)
	}
	t.Logf("picked %s (%d) focus %s tags %v", task.Title, task.Rating, task.Focus, task.Tags)
	starter, err := lcStarter(strings.TrimPrefix(task.ID, "lc:"))
	if err != nil || !strings.Contains(starter, "class Solution") {
		t.Fatalf("starter: %v %q", err, starter)
	}
	subs, err := lcSubs()
	if err != nil {
		t.Fatal("submissions:", err)
	}
	t.Logf("%d recent submissions", len(subs))
}

func TestGateToolHoldsWhileClosed(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	h, _ := os.UserHomeDir()
	cp := filepath.Join(h, "CP", "codeforces")
	os.MkdirAll(cp, 0o755)
	gems := filepath.Join(h, "Gems", "kinvault")
	os.MkdirAll(gems, 0o755)
	os.Symlink(gems, filepath.Join(cp, "sneaky"))
	cases := []struct {
		tool  string
		input map[string]any
		cwd   string
		deny  bool
	}{
		{"Read", map[string]any{"file_path": filepath.Join(gems, "main.go")}, cp, true},
		{"Read", map[string]any{"file_path": filepath.Join(cp, "2257/C.cpp")}, cp, false},
		{"Read", map[string]any{"file_path": filepath.Join(cp, "sneaky/main.go")}, cp, true},
		{"Edit", map[string]any{"file_path": filepath.Join(h, ".claude/settings.json")}, cp, true},
		{"Write", map[string]any{"file_path": filepath.Join(h, "CP/.claude/settings.local.json")}, cp, true},
		{"Grep", map[string]any{"pattern": "x"}, gems, true},
		{"Grep", map[string]any{"pattern": "x", "path": "../.."}, cp, true},
		{"Glob", map[string]any{"pattern": filepath.Join(gems, "**")}, cp, true},
		{"Glob", map[string]any{"pattern": "**/*.cpp"}, cp, false},
		{"Bash", map[string]any{"command": "g++ -std=c++20 -O2 -I ~/CP/lib 2257/C.cpp -o 2257/C && ./2257/C < 2257/.testcases/C_in_0.txt"}, cp, false},
		{"Bash", map[string]any{"command": "cd 2257 && g++ C.cpp -o C"}, cp, false},
		{"Bash", map[string]any{"command": "cd ~/Gems/kinvault && go test ./..."}, cp, true},
		{"Bash", map[string]any{"command": "cd .. && cat Gems/kinvault/main.go"}, cp, true},
		{"Bash", map[string]any{"command": "cat 2257/../../../Gems/kinvault/main.go"}, cp, true},
		{"Bash", map[string]any{"command": "git -C ../.. status"}, cp, true},
		{"Bash", map[string]any{"command": "ls .."}, cp, false}, // ~/CP itself
		{"Bash", map[string]any{"command": "git --git-dir=/home/x/Gems/kinvault/.git log"}, cp, true},
		{"Bash", map[string]any{"command": "cd; ls Gems"}, cp, true},
		{"Bash", map[string]any{"command": "echo Y2F0IH4vLnNzaC9pZF9yc2E= | base64 -d | sh"}, cp, true},
		{"Bash", map[string]any{"command": "python3 -c 'print(1)'"}, cp, true},
		{"Bash", map[string]any{"command": "bash x.sh"}, cp, true},
		{"Bash", map[string]any{"command": "systemctl --user stop edge"}, cp, true},
		{"Bash", map[string]any{"command": "script -qc 'edge pass' /dev/null"}, cp, true},
		{"Bash", map[string]any{"command": "go test ./..."}, gems, true},
		{"Bash", map[string]any{"command": "edge status"}, gems, false},
		{"Bash", map[string]any{"command": "edge status\ncat ~/.ssh/id_rsa"}, gems, true},
		{"Bash", map[string]any{"command": "edge status; rm -rf ~/Gems"}, cp, true},
		{"Bash", map[string]any{"command": "echo $SECRET_DIR/x"}, cp, true},
		{"Bash", map[string]any{"command": "curl -s https://codeforces.com/api/user.info?handles=screenager"}, cp, false},
		{"mcp__claude_ai_Vercel__create_deployment", map[string]any{}, cp, true},
		{"Monitor", map[string]any{"command": "cat ~/.ssh/id_rsa"}, cp, true},
		{"LSP", map[string]any{}, cp, true},
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
	if v := gateDecide(s, "help with kinvault", filepath.Join(h, "CP")); v.Block || !strings.Contains(v.Context, "GATE CLOSED") {
		t.Fatal("~/CP passes with the practice-only contract")
	}
	solved(s, "cf:1A", "solo", wed.Add(-time.Hour), wed)
	if v := gateDecide(s, "help with kinvault", filepath.Join(h, "CP")); strings.Contains(v.Context, "GATE CLOSED") {
		t.Fatal("open gate: no practice-only contract")
	}
}

func TestConfigFile(t *testing.T) {
	writeConfig(t, `# mine
[accounts]
codeforces = "tourist"  # a comment
[schedule]
weekday_from = "18:30"
day_ends = "11:00"   # too late: ignored
[quota]
weekday = 2
[problems]
sources = ["cf"]
any_topic_pct = 50
[focus.cf]
"number theory" = 4
[hints]
ladder_min = [10, 20, 30]
[notify]
drift_sites = ["hackernews"]
[launch]
terminal = "kitty"
[nope]
x = 1
`)
	c := cfg()
	if c.CFUser != "tourist" || c.LCUser != "screenager" || c.WeekdayFrom != 18*time.Hour+30*time.Minute || c.DayEnds != 4*time.Hour {
		t.Fatalf("accounts/schedule: %+v", c)
	}
	if c.WeekdayMin != 2 || len(c.Sources) != 1 || c.AnyTopic != 0.5 || c.Terminal != "kitty" {
		t.Fatalf("quota/problems/launch: %+v", c)
	}
	if len(c.Focus["cf"]) != 1 || c.Focus["cf"]["number theory"] != 4 || len(c.Focus["lc"]) == 0 {
		t.Fatalf("a [focus.cf] section replaces only cf's list: %v", c.Focus)
	}
	if c.LadderAt[0] != 10*time.Minute || !c.Distraction.MatchString("Hackernews — Zen") || c.Distraction.MatchString("YouTube") {
		t.Fatal("hints/notify")
	}
	if len(confErrs) != 2 {
		t.Fatalf("bad day_ends and the unknown key are reported: %v", confErrs)
	}
}

func TestConfigCantLoosenTheGateToday(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.pin() // the daemon saw today with the defaults
	writeConfig(t, "[gate]\nenabled = false\n[quota]\nweekday = 0\n")
	if !gateDecide(s, "x", "/tmp").Block {
		t.Fatal("switching the gate off mid-day must wait until tomorrow")
	}
	at(t, wed.AddDate(0, 0, 1))
	s.pin()
	if gateDecide(s, "x", "/tmp").Block {
		t.Fatal("tomorrow the new config applies")
	}
	writeConfig(t, "[quota]\nweekday = 2\n")
	solved(s, "cf:1A", "solo", now().Add(-time.Hour), now())
	if !gateDecide(s, "x", "/tmp").Block {
		t.Fatal("stricter rules apply right away")
	}
	writeConfig(t, "[gate]\nworkspace = \"~\"\n")
	if cfg().Workspace == os.Getenv("HOME") {
		t.Fatal("the whole home can't be the workspace")
	}
}

const settingsFixture = `{
  "model": "opus",
  "statusLine": {"type": "command", "command": "a <b> & c"},
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "/old/path/edge gate", "timeout": 10}]}],
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "\"/old path/edge\" hook"}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "ember hook"}]}
    ],
    "Notification": {"weird": "not an array"}
  },
  "permissions": {"allow": ["Bash(ls:*)"]}
}`

func TestPatchSettings(t *testing.T) {
	exe := "/home/x/.local/bin/edge"
	out, err := patchSettings([]byte(settingsFixture), exe)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := patchSettings(out, exe)
	if string(again) != string(out) {
		t.Fatal("patch must be idempotent")
	}
	var before, after map[string]any
	json.Unmarshal([]byte(settingsFixture), &before)
	json.Unmarshal(out, &after)
	for k, v := range before {
		if k == "hooks" || k == "permissions" {
			continue
		}
		if fmt.Sprint(after[k]) != fmt.Sprint(v) {
			t.Errorf("changed unrelated key %s", k)
		}
	}
	s := string(out)
	if strings.Contains(s, "edge gate") || strings.Contains(s, "old path") || strings.Count(s, exe+" hook") != 2 {
		t.Fatalf("want 2 gate entries and no old edge entries:\n%s", s)
	}
	if !strings.Contains(s, "ember hook") || !strings.Contains(s, "not an array") || !strings.Contains(s, "a <b> & c") {
		t.Fatalf("other hooks and odd values must survive:\n%s", s)
	}
}

func TestContestOf(t *testing.T) {
	for id, want := range map[string]string{"cf:1360E": "1360", "cf:1360E1": "1360", "cf:2247D2": "2247", "cf:4A": "4"} {
		if got := contestOf(id); got != want {
			t.Errorf("contestOf(%s) = %s, want %s", id, got, want)
		}
	}
	if p := solutionPath(&Task{ID: "cf:1360E1"}); !strings.HasSuffix(p, "codeforces/1360/E1.cpp") {
		t.Errorf("path %s", p)
	}
}

func TestStateRecoversFromCorruptFile(t *testing.T) {
	at(t, wed)
	fresh(t)
	if err := update(func(s *State) error { s.Ratings["cf"] = 1234; return nil }); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(statePath())
	os.WriteFile(statePath()+".bak", good, 0o644)
	os.WriteFile(statePath(), []byte(`{"history": [tru`), 0o644)
	s, err := load()
	if err != nil {
		t.Fatal("a corrupt file must fall back to the backup:", err)
	}
	if s.Ratings["cf"] != 1234 || s.Trouble["state"].Msg == "" {
		t.Fatalf("restored %v, trouble %v", s.Ratings, s.Trouble)
	}
	if err := update(func(s *State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, raw, err := loadRaw(); err != nil || raw == nil {
		t.Fatal("update must repair state.json")
	}
}

func TestUpdateSkipsUnchangedWrites(t *testing.T) {
	at(t, wed)
	fresh(t)
	update(func(s *State) error { s.Ratings["cf"] = 1; return nil })
	st1, _ := os.Stat(statePath())
	time.Sleep(10 * time.Millisecond)
	update(func(s *State) error { return nil })
	st2, _ := os.Stat(statePath())
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("a no-op update rewrote state.json")
	}
}

func TestDefaultConfigTextMatchesDefaults(t *testing.T) {
	writeConfig(t, defaultConfigText)
	c, d := cfg(), defaultConfig()
	if len(confErrs) != 0 {
		t.Fatalf("the shipped config must parse cleanly: %v", confErrs)
	}
	if fmt.Sprint(c.Focus) != fmt.Sprint(d.Focus) || c.Distraction.String() != d.Distraction.String() ||
		fmt.Sprint(c.LadderAt, c.Sources, c.Rating) != fmt.Sprint(d.LadderAt, d.Sources, d.Rating) {
		t.Fatal("config.toml as shipped must mean exactly the defaults")
	}
	c.Focus, d.Focus, c.Distraction, d.Distraction, c.LadderAt, d.LadderAt, c.Sources, d.Sources, c.Rating, d.Rating = nil, nil, nil, nil, nil, nil, nil, nil, nil, nil
	if fmt.Sprintf("%+v", *c) != fmt.Sprintf("%+v", *d) {
		t.Fatalf("shipped text differs from defaults:\n%+v\n%+v", *c, *d)
	}
}

// The daemon records an AC and hands out the next task before the user asks
// for the explain-back: done must still find the one that's waiting.
func TestDoneFindsExplainBackAfterNextTask(t *testing.T) {
	s := fresh(t)
	at(t, wed)
	s.History = append(s.History, Task{ID: "cf:1989A", Kind: "new", Title: "Catch the Coin", Outcome: "solo", Explain: -1,
		Started: wed.Add(-time.Hour), Finished: wed.Add(-time.Minute)})
	s.Current = &Task{ID: "cf:2261B", Kind: "upsolve", Title: "Capital", Assigned: wed, Explain: -1}
	var buf strings.Builder
	out = &buf
	t.Cleanup(func() { out = os.Stdout })

	if err := done(s, false, nil); err != nil {
		t.Fatalf("done refused a waiting explain-back: %v", err)
	}
	if !strings.Contains(buf.String(), "EXPLAIN-BACK for Catch the Coin") {
		t.Fatalf("wrong explain-back: %q", buf.String())
	}
	if s.Current == nil || s.Current.ID != "cf:2261B" {
		t.Fatal("done must leave the unaccepted current task alone")
	}
	buf.Reset()
	if err := explain(s, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "recorded 1/2 for Catch the Coin") {
		t.Fatalf("explain didn't name the task: %q", buf.String())
	}
	if err := done(s, false, nil); err == nil || !strings.Contains(err.Error(), "no accepted submission for Capital") {
		t.Fatalf("with nothing waiting, done should ask for a submission: %v", err)
	}
	if err := done(s, false, fmt.Errorf("cf down")); err == nil || err.Error() != "cf down" {
		t.Fatalf("a failed check should say so: %v", err)
	}
}

func TestArgsChecked(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		args []string
		ok   bool
	}{
		{"done", nil, true}, {"done", []string{"--dry-run"}, false}, {"next", []string{"x"}, false},
		{"stats", []string{"--compact"}, true}, {"stats", []string{"-c"}, false},
		{"skip", []string{"--seen"}, true}, {"stuck", []string{"--take", "2"}, true}, {"stuck", []string{"2"}, false},
		{"explain", []string{"1"}, true}, {"explain", []string{"1", "2"}, false},
		{"round", []string{"lesson", "a", "b"}, true}, {"action", []string{"explain"}, true},
	} {
		if got := argsOK(c.cmd, c.args); got != c.ok {
			t.Errorf("argsOK(%s %v) = %v, want %v", c.cmd, c.args, got, c.ok)
		}
	}
}
