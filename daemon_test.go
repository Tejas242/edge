package main

import (
	"strings"
	"testing"
	"time"
)

func keys(evs []event) string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Key)
	}
	return strings.Join(k, " ")
}

func pending(s *State) *Task {
	s.Current = &Task{ID: "cf:2257C", Kind: "new", Title: "Spying on the Beaver", Policy: "ladder", Arm: "ladder", Assigned: now(), Explain: -1}
	return s.Current
}

func TestOfferPendingTaskAndSnooze(t *testing.T) {
	at(t, wed) // 21:00, study time
	s := fresh(t)
	pending(s)
	evs := s.filter(plan(s, window{}))
	if !strings.Contains(keys(evs), "offer:cf:2257C") {
		t.Fatalf("pending task should be offered: %s", keys(evs))
	}
	if strings.Contains(keys(s.filter(plan(s, window{}))), "offer") {
		t.Fatal("offer must not repeat within 30 min")
	}
	at(t, wed.Add(31*time.Minute))
	if !strings.Contains(keys(s.filter(plan(s, window{}))), "offer") {
		t.Fatal("offer should repeat after 30 min")
	}
	s.Snooze = now().Add(30 * time.Minute)
	at(t, wed.Add(50*time.Minute))
	if strings.Contains(keys(plan(s, window{})), "offer") {
		t.Fatal("snoozed: no offer")
	}
}

func TestNoOfferOutsideStudyTime(t *testing.T) {
	at(t, time.Date(2026, 10, 14, 11, 0, 0, 0, time.Local)) // Wednesday 11:00, at work
	s := fresh(t)
	pending(s)
	if strings.Contains(keys(plan(s, window{})), "offer") {
		t.Fatal("weekday daytime is work time")
	}
}

func TestHintUnlockNotifications(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	begin(task, wed)
	at(t, wed.Add(21*time.Minute))
	if k := keys(s.filter(plan(s, window{}))); !strings.Contains(k, "hint:cf:2257C:1") {
		t.Fatalf("level 1 at 21m: %s", k)
	}
	at(t, wed.Add(22*time.Minute))
	if k := keys(s.filter(plan(s, window{}))); strings.Contains(k, "hint") {
		t.Fatalf("no repeat: %s", k)
	}
	at(t, wed.Add(36*time.Minute))
	if k := keys(s.filter(plan(s, window{}))); !strings.Contains(k, "hint:cf:2257C:2") || strings.Contains(k, ":1") {
		t.Fatalf("only level 2 at 36m: %s", k)
	}
	at(t, wed.Add(61*time.Minute))
	if k := keys(s.filter(plan(s, window{}))); !strings.Contains(k, "giveup:cf:2257C") {
		t.Fatalf("give-up offer at 61m: %s", k)
	}
}

func TestDriftDetection(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	begin(task, wed)
	yt := window{App: "zen", Title: "lofi beats - YouTube — Zen Browser"}
	if k := keys(s.filter(plan(s, yt))); strings.Contains(k, "drift") {
		t.Fatal("a glance isn't drift")
	}
	at(t, wed.Add(3*time.Minute))
	if k := keys(s.filter(plan(s, yt))); !strings.Contains(k, "drift") {
		t.Fatalf("3 min on YouTube is drift: %s", k)
	}
	at(t, wed.Add(9*time.Minute))
	plan(s, window{App: "Alacritty", Title: "nvim C.cpp"})
	if task.DriftMin != 9 || !s.DriftAt.IsZero() {
		t.Fatalf("drift minutes %d", task.DriftMin)
	}
	if distraction.MatchString("Codeforces Round 1000 — Zen Browser") || distraction.MatchString("nvim box.cpp") {
		t.Fatal("false positive")
	}
}

func TestProcessCFVerdicts(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	p := cfProblem{ContestID: 2257, Index: "C"}
	base := wed.Unix()
	subs := []cfSub{ // newest first, as the API returns
		{Problem: p, Verdict: "TESTING", Time: base + 300},
		{Problem: p, Verdict: "WRONG_ANSWER", Passed: 2, Time: base + 200},
		{Problem: p, Verdict: "WRONG_ANSWER", Passed: 1, Time: base + 100},
		{Problem: cfProblem{ContestID: 1, Index: "A"}, Verdict: "OK", Time: base + 50},
	}
	evs := processCF(s, task, subs)
	if task.Fails != 2 || !task.Active() || task.SubSeen != base+200 {
		t.Fatalf("fails %d active %v seen %d", task.Fails, task.Active(), task.SubSeen)
	}
	if len(evs) != 2 || evs[1].Title != "Wrong answer on test 3" {
		t.Fatalf("events %+v", evs)
	}
	subs[0].Verdict = "OK"
	evs = processCF(s, task, subs)
	if s.Current != nil || len(s.History) != 1 || s.History[0].Outcome != "solo" || evs[0].Key != "ac:cf:2257C" {
		t.Fatalf("AC not recorded: %+v %+v", s.History, evs)
	}
	if got, _ := s.today(); got != 1 {
		t.Fatal("AC should earn today's credit")
	}
}

func TestLateWarningOnlyWhenGated(t *testing.T) {
	late := time.Date(2026, 10, 14, 22, 30, 0, 0, time.Local)
	at(t, late)
	s := fresh(t)
	s.GateFrom = day(late).AddDate(0, 0, 1)
	if strings.Contains(keys(plan(s, window{})), "late") {
		t.Fatal("gate not active yet")
	}
	s.GateFrom = time.Time{}
	if !strings.Contains(keys(plan(s, window{})), "late") {
		t.Fatal("should warn at 22:30 with work left")
	}
}

func TestWireCardPutsPrimaryRight(t *testing.T) {
	c := emberCardFor(event{Key: "offer:cf:1A", Title: "x", Actions: []action{{"start", "Start"}, {"snooze", "In 30 min"}, {"rest", "Done for today"}}})
	if n := len(c.Buttons); n != 3 || c.Buttons[n-1].Key != "start" || !c.Buttons[n-1].Primary || c.Buttons[0].Primary {
		t.Fatalf("%+v", c.Buttons)
	}
	if c.Mood != "work" || c.ID != "offer" {
		t.Fatalf("%+v", c)
	}
}
