package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

// tell plans, filters and marks as delivered: what the user would see.
func tell(s *State, w window, dt time.Duration) []event {
	evs := s.filter(plan(s, w, dt))
	var said []string
	for _, e := range evs {
		said = append(said, e.Key)
	}
	s.markSaid(said)
	return evs
}

func pending(s *State) *Task {
	s.Current = &Task{ID: "cf:2257C", Kind: "new", Title: "Spying on the Beaver", Assigned: now(), Explain: -1}
	return s.Current
}

func TestOfferPendingTaskAndSnooze(t *testing.T) {
	at(t, wed) // 21:00, study time
	s := fresh(t)
	pending(s)
	if k := keys(tell(s, window{}, 0)); !strings.Contains(k, "offer:cf:2257C") {
		t.Fatalf("pending task should be offered: %s", k)
	}
	if strings.Contains(keys(tell(s, window{}, 0)), "offer") {
		t.Fatal("offer must not repeat within 30 min")
	}
	at(t, wed.Add(31*time.Minute))
	if !strings.Contains(keys(tell(s, window{}, 0)), "offer") {
		t.Fatal("offer should repeat after 30 min")
	}
	if s.Current.Offers != 2 {
		t.Fatalf("offers counted: %d", s.Current.Offers)
	}
	s.Snooze = now().Add(30 * time.Minute)
	at(t, wed.Add(50*time.Minute))
	if strings.Contains(keys(plan(s, window{}, 0)), "offer") {
		t.Fatal("snoozed: no offer")
	}
}

func TestUndeliveredEventIsRetried(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	pending(s)
	if !strings.Contains(keys(s.filter(plan(s, window{}, 0))), "offer") {
		t.Fatal("offer due")
	}
	// nothing marked: delivery failed
	if !strings.Contains(keys(s.filter(plan(s, window{}, 0))), "offer") {
		t.Fatal("an offer that never reached the screen must be tried again")
	}
}

func TestNoOfferOutsideStudyTime(t *testing.T) {
	at(t, time.Date(2026, 10, 14, 11, 0, 0, 0, time.Local)) // Wednesday 11:00, at work
	s := fresh(t)
	pending(s)
	if strings.Contains(keys(plan(s, window{}, 0)), "offer") {
		t.Fatal("weekday daytime is work time")
	}
}

func TestStaleOfferIsShelved(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	task.Offers = 3
	at(t, wed.AddDate(0, 0, 1))
	if !s.stale() || !s.wantsAssign() {
		t.Fatal("offered yesterday, never started: replace it")
	}
	shelve(s)
	if s.Current != nil || s.History[0].Outcome != "ignored" || credit(s.History[0]) != 0 {
		t.Fatalf("%+v", s.History)
	}
	if s.offered("cf:2257C") == false {
		t.Fatal("an ignored problem isn't offered again as new")
	}
	s.Current = &Task{ID: "cf:1A", Kind: "resolve", ProbeStage: 1, Arm: "ladder", Assigned: wed, Offers: 3}
	shelve(s)
	if len(s.Probes) != 1 || s.Probes[0].Problem != "cf:1A" {
		t.Fatal("a shelved re-solve goes back on the schedule")
	}
}

func TestHintUnlockNotificationsFollowWork(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	begin(s, task, wed)
	task.Policy, task.Arm = "ladder", "ladder"
	at(t, wed.Add(3*time.Hour)) // lots of wall-clock time, little work
	task.WorkSec = 5 * 60
	if k := keys(tell(s, window{}, 0)); strings.Contains(k, "hint") || strings.Contains(k, "giveup") {
		t.Fatalf("hours away don't unlock hints: %s", k)
	}
	task.WorkSec = 21 * 60
	if k := keys(tell(s, window{}, 0)); !strings.Contains(k, "hint:cf:2257C:1") {
		t.Fatalf("level 1 at 21m of work: %s", k)
	}
	if k := keys(tell(s, window{}, 0)); strings.Contains(k, "hint") {
		t.Fatalf("no repeat: %s", k)
	}
	task.WorkSec = 36 * 60
	if k := keys(tell(s, window{}, 0)); !strings.Contains(k, "hint:cf:2257C:2") || strings.Contains(k, ":1") {
		t.Fatalf("only level 2 at 36m: %s", k)
	}
	task.WorkSec = 61 * 60
	if k := keys(tell(s, window{}, 0)); !strings.Contains(k, "giveup:cf:2257C") {
		t.Fatalf("give-up offer at 61m: %s", k)
	}
}

func TestBeginPicksPolicyAtStart(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	if task.Policy != "" {
		t.Fatal("no arm before starting: skipping can't steer the experiment")
	}
	begin(s, task, wed)
	if task.Policy != "ladder" && task.Policy != "instant" || task.Arm != task.Policy {
		t.Fatalf("policy %q arm %q", task.Policy, task.Arm)
	}
}

func TestWorkTracking(t *testing.T) {
	task := &Task{Title: "Spying on the Beaver"}
	for _, c := range []struct {
		w     window
		fresh bool
		want  bool
	}{
		{window{App: "Alacritty", Title: "nvim C.cpp"}, false, true},
		{window{App: "zen", Title: "Problem - 2257C - Codeforces — Zen Browser"}, false, true},
		{window{App: "zen", Title: "Spying on the Beaver - LeetCode"}, false, true},
		{window{App: "zen", Title: "lofi beats - YouTube — Zen Browser"}, false, false},
		{window{App: "zen", Title: "lofi beats - YouTube — Zen Browser"}, true, true},
		{window{}, false, false},
	} {
		if got := working(c.w, task, c.fresh); got != c.want {
			t.Errorf("%+v fresh=%v: %v", c.w, c.fresh, got)
		}
	}
}

func TestDriftDetection(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	begin(s, task, wed)
	yt := window{App: "zen", Title: "lofi beats - YouTube — Zen Browser"}
	if k := keys(tell(s, yt, 0)); strings.Contains(k, "drift") {
		t.Fatal("a glance isn't drift")
	}
	at(t, wed.Add(3*time.Minute))
	if k := keys(tell(s, yt, 20*time.Second)); !strings.Contains(k, "drift") {
		t.Fatalf("3 min on YouTube is drift: %s", k)
	}
	plan(s, window{App: "Alacritty", Title: "nvim C.cpp"}, 20*time.Second)
	if task.DriftSec != 20 || !s.DriftAt.IsZero() {
		t.Fatalf("drift is counted per tick seen: %d", task.DriftSec)
	}
	// finishing while on YouTube must not leak into tomorrow's task
	plan(s, yt, 20*time.Second)
	out = io.Discard
	t.Cleanup(func() { out = os.Stdout })
	accept(s, task, now())
	if !s.DriftAt.IsZero() {
		t.Fatal("drift timer survives the task")
	}
	at(t, wed.AddDate(0, 0, 1))
	next := pending(s)
	begin(s, next, now())
	if k := keys(tell(s, yt, 20*time.Second)); strings.Contains(k, "drift") || next.DriftSec != 20 {
		t.Fatalf("a new task starts with a clean drift slate: %s %d", k, next.DriftSec)
	}
	if cfg().Distraction.MatchString("Codeforces Round 1000 — Zen Browser") || cfg().Distraction.MatchString("nvim box.cpp") {
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
	processCF(s, task, subs)
	if s.Current != nil || len(s.History) != 1 || s.History[0].Outcome != "solo" {
		t.Fatalf("AC not recorded: %+v", s.History)
	}
	if k := keys(tell(s, window{}, 0)); !strings.Contains(k, "ac:cf:2257C") {
		t.Fatalf("the Accepted card comes from plan, so it survives a failed delivery: %s", k)
	}
	if got, _ := s.today(); got != 1 {
		t.Fatal("AC should earn today's credit")
	}
}

func TestProcessLCVerdicts(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.Current = &Task{ID: "lc:sum-game", Kind: "new", Title: "Sum Game", Assigned: wed, Explain: -1}
	ts := func(d time.Duration) string { return strconv.FormatInt(wed.Add(d).Unix(), 10) }
	subs := []lcSub{
		{Slug: "sum-game", Time: ts(30 * time.Minute), Status: "Accepted"},
		{Slug: "sum-game", Time: ts(20 * time.Minute), Status: "Wrong Answer"},
		{Slug: "two-sum", Time: ts(10 * time.Minute), Status: "Accepted"},
		{Slug: "sum-game", Time: ts(-time.Hour), Status: "Accepted"}, // before it was assigned
	}
	evs := processLC(s, s.Current, subs)
	if len(evs) != 1 || evs[0].Title != "Wrong Answer" {
		t.Fatalf("LC wrong answers are seen too: %+v", evs)
	}
	if len(s.History) != 1 || s.History[0].Fails != 1 || s.History[0].Outcome != "solo" {
		t.Fatalf("%+v", s.History)
	}
}

func TestLateWarningOnlyWhenGated(t *testing.T) {
	late := time.Date(2026, 10, 14, 22, 30, 0, 0, time.Local)
	at(t, late)
	s := fresh(t)
	s.GateFrom = day(late).AddDate(0, 0, 1)
	if strings.Contains(keys(plan(s, window{}, 0)), "late") {
		t.Fatal("gate not active yet")
	}
	s.GateFrom = day(late).AddDate(0, 0, -1)
	if !strings.Contains(keys(plan(s, window{}, 0)), "late") {
		t.Fatal("should warn at 22:30 with work left")
	}
}

func TestHealthIsSaid(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	s.trouble("gate", "hook missing")
	s.trouble("cf", "codeforces down")
	if k := keys(tell(s, window{}, 0)); !strings.Contains(k, "health:gate") || strings.Contains(k, "health:cf") {
		t.Fatalf("gate trouble at once, a judge blip only once it lasts: %s", k)
	}
	at(t, wed.Add(31*time.Minute))
	if k := keys(tell(s, window{}, 0)); !strings.Contains(k, "health:cf") || strings.Contains(k, "health:gate") {
		t.Fatalf("judge down 30m is said, gate isn't repeated the same day: %s", k)
	}
}

func TestSyncBacksOff(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	if !s.syncDue("cf") {
		t.Fatal("never synced")
	}
	s.applySync(syncData{Src: "cf", Err: errors.New("offline")})
	if s.syncDue("cf") {
		t.Fatal("a failed sync must not retry on the next tick")
	}
	at(t, wed.Add(61*time.Second))
	if !s.syncDue("cf") {
		t.Fatal("retry after a minute")
	}
	for i := 0; i < 10; i++ {
		s.applySync(syncData{Src: "cf", Err: errors.New("offline")})
	}
	at(t, wed.Add(59*time.Minute))
	if s.syncDue("cf") {
		t.Fatal("backoff grows to an hour")
	}
	s.applySync(syncData{Src: "cf", Rating: 1300, CFSubs: []cfSub{{Problem: cfProblem{ContestID: 5, Index: "A"}, Verdict: "OK", Time: 1}}})
	if s.syncDue("cf") || s.Ratings["cf"] != 1300 || s.CFDone["cf:5A"] != 1 || s.leg("cf").Fails != 0 {
		t.Fatal("success resets")
	}
	if !s.syncDue("lc") {
		t.Fatal("each source on its own schedule")
	}
}

func TestCacheKeepsGoodCopyOverBadFetch(t *testing.T) {
	at(t, wed)
	fresh(t)
	valid := func(b []byte) error {
		if string(b) != "good" {
			return errors.New("bad")
		}
		return nil
	}
	if b, err := cached("x", time.Hour, func() ([]byte, error) { return []byte("good"), nil }, valid); err != nil || string(b) != "good" {
		t.Fatal(err)
	}
	at(t, wed.Add(2*time.Hour))
	b, err := cached("x", time.Hour, func() ([]byte, error) { return []byte("<html>maintenance</html>"), nil }, valid)
	if err != nil || string(b) != "good" {
		t.Fatalf("a 200 that isn't data must not replace the cache: %q %v", b, err)
	}
	if got, _ := os.ReadFile(filepath.Join(cacheDir(), "x")); string(got) != "good" {
		t.Fatal("cache overwritten")
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
