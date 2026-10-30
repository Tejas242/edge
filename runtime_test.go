package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClock drives a sensor's monotonic clock by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func testSensor(c *fakeClock) *sensor    { x := newSensor(); x.clock = c.now; return x }
func focusEv(id uint64) niriEvent {
	e := niriEvent{}
	e.WindowFocusChanged = &struct {
		ID *uint64 `json:"id"`
	}{&id}
	return e
}
func winEv(w niriWin) niriEvent {
	e := niriEvent{}
	e.WindowOpenedOrChanged = &struct {
		Window niriWin `json:"window"`
	}{w}
	return e
}
func winsEv(ws ...niriWin) niriEvent {
	e := niriEvent{}
	e.WindowsChanged = &struct {
		Windows []niriWin `json:"windows"`
	}{ws}
	return e
}
func countTask(title string) senseTask {
	return senseTask{ID: "cf:2277C", Title: title, Count: true, Path: "/nonexistent/C.cpp", WS: "/ws"}
}
func (x *sensor) focusedNow() window { x.mu.Lock(); defer x.mu.Unlock(); return x.focusedLocked() }
func (x *sensor) takeAt(c *fakeClock, st senseTask) (int, bool) {
	s, _, on := x.take(st, window{})
	return s, on
}

// The stream keeps the focused window; work is integrated between events on
// the monotonic clock, only for the task that counts, and only on the screen
// that means work.
func TestSensorCountsWorkFromEvents(t *testing.T) {
	at(t, wed)
	c := &fakeClock{t: time.Unix(1000, 0)}
	x := testSensor(c)
	x.niri(winsEv(niriWin{ID: 1, AppID: "zen", Title: "lofi - YouTube — Zen Browser", Focused: true},
		niriWin{ID: 2, AppID: "zen", Title: "Problem - 2277C - Codeforces — Zen Browser"}))
	if x.focusedNow().ID != 1 {
		t.Fatal("focus from WindowsChanged")
	}
	task := countTask("Spying on the Beaver")
	if sec, on := x.takeAt(c, task); sec != 0 || on {
		t.Fatalf("YouTube isn't work: %d %v", sec, on)
	}
	c.add(30 * time.Second)
	x.niri(focusEv(2)) // to the problem
	c.add(90 * time.Second)
	sec, on := x.takeAt(c, task)
	if sec != 90 || !on {
		t.Fatalf("90 s on the problem: %d %v", sec, on)
	}
	c.add(10 * time.Second)
	x.niri(focusEv(1))
	c.add(10 * time.Minute) // away: nothing counts, however long
	if sec, on := x.takeAt(c, task); sec != 10 || on {
		t.Fatalf("only the 10 s before leaving: %d %v", sec, on)
	}
	// another task drops what was counted for the old one
	x.niri(focusEv(2))
	c.add(time.Minute)
	other := task
	other.ID = "cf:1A"
	if sec, _ := x.takeAt(c, other); sec != 0 {
		t.Fatalf("the old task's minute leaked into the new one: %d", sec)
	}
	// a task that doesn't count (unstarted, or a live round) gets nothing
	other.Count = false
	c.add(time.Minute)
	if sec, on := x.takeAt(c, other); sec != 0 || on {
		t.Fatalf("not counting: %d %v", sec, on)
	}
}

func TestSensorSubmitSignalAndWake(t *testing.T) {
	at(t, wed)
	c := &fakeClock{t: time.Unix(1000, 0)}
	x := testSensor(c)
	x.niri(winsEv(niriWin{ID: 2, AppID: "zen", Title: "Problem - 2277C - Codeforces — Zen Browser", Focused: true}))
	x.take(countTask("x"), window{})
	if !x.submitted().IsZero() {
		t.Fatal("no title change yet")
	}
	x.niri(winEv(niriWin{ID: 9, AppID: "Alacritty", Title: "✳ claude"})) // a background window: nothing
	if !x.submitted().IsZero() {
		t.Fatal("a background window's title isn't a submit")
	}
	c.add(time.Second)
	x.niri(winEv(niriWin{ID: 2, AppID: "zen", Title: "Status - Codeforces — Zen Browser", Focused: true}))
	if x.submitted() != c.t {
		t.Fatal("the judge tab's title change is the submit signal")
	}
	select {
	case <-x.wake:
	default:
		t.Fatal("a submit wakes the daemon")
	}
}

// A terminal's cwd is read when it gains focus, then cached: Claude's
// spinner retitling the window every few hundred ms costs nothing.
func TestSensorCachesTerminalCwd(t *testing.T) {
	at(t, wed)
	calls := 0
	old := procCwds
	t.Cleanup(func() { procCwds = old })
	procCwds = func(pid int) []string { calls++; return []string{"/ws/codeforces"} }
	c := &fakeClock{t: time.Unix(1000, 0)}
	x := testSensor(c)
	x.niri(winsEv(niriWin{ID: 1, AppID: "zen", Title: "x", Focused: true}, niriWin{ID: 3, AppID: "Alacritty", Title: "nvim", PID: 7}))
	x.take(countTask("x"), window{})
	x.niri(focusEv(3))
	for i := range 50 {
		c.add(200 * time.Millisecond)
		x.niri(winEv(niriWin{ID: 3, AppID: "Alacritty", Title: "nvim " + strings.Repeat("·", i%4), PID: 7, Focused: true}))
	}
	if calls != 1 {
		t.Fatalf("cwd read %d times for one focus", calls)
	}
	if sec, on := x.takeAt(c, countTask("x")); !on || sec != 10 {
		t.Fatalf("a terminal in the workspace is work: %d %v", sec, on)
	}
	c.add(cwdFresh)
	x.takeAt(c, countTask("x"))
	if calls != 2 {
		t.Fatalf("the cache lasts a minute: %d reads", calls)
	}
}

// inotify sees the problem file arrive and saves to it; a fresh save is work.
func TestSensorWatchesWorkspace(t *testing.T) {
	ws := t.TempDir()
	x := newSensor()
	go x.runInotify(ws)
	waitFor(t, func() bool { x.mu.Lock(); defer x.mu.Unlock(); return x.inoUp })
	sol := filepath.Join(ws, "codeforces", "2277", "C.cpp")
	if _, ok := x.fileTime(sol); ok {
		t.Fatal("no file yet")
	}
	st := senseTask{ID: "cf:2277C", Path: sol, WS: ws} // unstarted
	x.take(st, window{})
	os.MkdirAll(filepath.Dir(sol), 0o755)
	os.WriteFile(sol, []byte("int main(){}"), 0o644) // competitest: dir and file together
	waitFor(t, func() bool { _, ok := x.fileTime(sol); return ok })
	select {
	case <-x.wake:
	case <-time.After(2 * time.Second):
		t.Fatal("the problem arriving wakes the daemon")
	}
	st.Count = true
	if _, _, on := x.take(st, window{}); !on {
		t.Fatal("a solution saved just now is work")
	}
	os.Remove(sol)
	waitFor(t, func() bool { _, ok := x.fileTime(sol); return !ok })
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatal("timed out")
}

func TestWorkStateWorkingAndPaused(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	if w, p := workState(s, task); w || p {
		t.Fatal("unstarted: neither")
	}
	task.Started = wed.Add(-3 * time.Hour)
	task.WorkAt = wed.Add(-20 * time.Second)
	if w, p := workState(s, task); !w || p {
		t.Fatalf("work 20 s ago: working (%v %v)", w, p)
	}
	task.WorkAt = wed.Add(-30 * time.Minute)
	if w, p := workState(s, task); w || p {
		t.Fatalf("30 min away: neither (%v %v)", w, p)
	}
	task.WorkAt = wed.Add(-2 * time.Hour)
	if _, p := workState(s, task); !p {
		t.Fatal("2 h without work: paused")
	}
	task.Started, task.WorkAt = wed.Add(-26*time.Hour), wed.Add(-20*time.Hour+5*time.Minute) // started yesterday evening, worked till 01:05
	at(t, wed.Add(-19*time.Hour+5*time.Minute))                                              // 02:05 the same edge day
	if _, p := workState(s, task); p {
		t.Fatal("01:05 → 02:05 is the same edge day, an hour away: not paused")
	}
	at(t, wed.Add(-16*time.Hour)) // 05:00: past day_ends
	task.WorkAt = wed.Add(-17*time.Hour - 30*time.Minute)
	if _, p := workState(s, task); !p {
		t.Fatal("across day_ends: paused")
	}
}

func TestJudgePlan(t *testing.T) {
	n := wed
	var j judgePlan
	task := &Task{ID: "cf:2277C"}
	if j.due(task, n, n) {
		t.Fatal("unstarted: no polling")
	}
	task.Started = n.Add(-time.Hour)
	if j.due(task, n.Add(-11*time.Minute), n) {
		t.Fatal("no work for 10 min: no polling")
	}
	if !j.due(task, n.Add(-time.Minute), n) {
		t.Fatal("working: trickle")
	}
	j.last = n
	if j.due(task, n, n.Add(30*time.Second)) || !j.due(task, n, n.Add(pollEvery)) {
		t.Fatal("trickle every minute")
	}
	// a submit signal: a burst every 3 s; nothing pending ends it after a few polls
	j.signal(n.Add(time.Second), n.Add(time.Second))
	for i := range burstGrace {
		at := n.Add(time.Duration(i+1) * burstEvery)
		if !j.due(task, time.Time{}, at) {
			t.Fatalf("burst poll %d", i)
		}
		j.last = at
		j.polled("", at)
	}
	if j.left != 0 {
		t.Fatal("nothing pending: the burst stops")
	}
	j.signal(n.Add(12*time.Second), n.Add(12*time.Second))
	if j.left != 0 {
		t.Fatal("a burst doesn't restart straight away")
	}
	// TESTING seen by a trickle poll starts one, and it runs while judging, at most 40 polls
	j.polled("test 3", n.Add(time.Minute))
	polls := 0
	for ; j.left > 0; polls++ {
		j.polled("test 9", n)
	}
	if polls != burstMax {
		t.Fatalf("burst capped at %d, made %d", burstMax, polls)
	}
	if d := j.wait(time.Minute, n); d != time.Minute {
		t.Fatalf("no burst: the tick decides (%s)", d)
	}
}

func TestLiveJudgingAndVerdict(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	task := pending(s)
	begin(s, task, wed.Add(-time.Hour))
	sub := func(v string, passed int, at int64) cfSub {
		x := cfSub{Verdict: v, Passed: passed, Time: at}
		x.Problem.ContestID, x.Problem.Index = 2257, "C"
		return x
	}
	base := wed.Unix()
	processCF(s, task, []cfSub{sub("TESTING", 8, base)})
	if task.Judging != "test 9" || task.Fails != 0 {
		t.Fatalf("testing: %q", task.Judging)
	}
	task.Judging = ""
	processCF(s, task, []cfSub{sub("WRONG_ANSWER", 2, base), sub("", 0, base+60)})
	if task.Verdict != "WA on test 3" || task.Judging != "queue" {
		t.Fatalf("verdict %q judging %q", task.Verdict, task.Judging)
	}
	for in, want := range map[string]string{"WJ": "queue", "3/14": "test 4", "14/14": "test 14", "WA": "", "AC": ""} {
		if got := acJudging(in); got != want {
			t.Errorf("acJudging(%q) = %q, want %q", in, got, want)
		}
	}
}

// R9: AtCoder's task pages don't say "AtCoder"; the task's own title does it.
func TestAtCoderTabsCountAsWork(t *testing.T) {
	task := &Task{ID: "ac:abc400_d", Title: "Takahashi the Wall Breaker"}
	for title, want := range map[string]bool{
		"D - Takahashi the Wall Breaker — Zen Browser":       true,
		"Submissions - AtCoder Beginner Contest 400 — Zen":   true,
		"Tasks - AtCoder Beginner Contest 400 — Zen Browser": true,
		"C - Some Other Problem — Zen Browser":               false,
		"Takahashi the Wall Breaker - YouTube — Zen Browser": true, // the title is the title
	} {
		if got := working(window{App: "zen", Title: title}, task, false, "/ws"); got != want {
			t.Errorf("%q: %v", title, got)
		}
	}
}

// State is written only when something real changed: a tick that only
// looked (no work, no news) leaves state.json alone.
func TestIdleTickDoesNotWriteState(t *testing.T) {
	at(t, wed.Add(-12*time.Hour)) // 09:00: not study time
	s := fresh(t)
	task := pending(s)
	begin(s, task, now().Add(-time.Minute))
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	tick := func() {
		if err := update(func(s *State) error { step(s, window{}, gathered{}, busyTick); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	st0, _ := os.Stat(statePath())
	for range 3 {
		tick()
	}
	if st, _ := os.Stat(statePath()); !os.SameFile(st0, st) {
		t.Fatal("an idle tick rewrote state.json")
	}
}

func TestStatusKeyIgnoresHeartbeatAndRunningClock(t *testing.T) {
	a := emberStatus{TaskID: "cf:1A", Working: true, WorkSec: 600, AsOfMs: 1_000_000, SeenMs: 1}
	b := a
	b.WorkSec, b.AsOfMs, b.SeenMs = 620, 1_020_000, 2 // 20 s later, still working
	ka, aa := statusKey(a)
	kb, ab := statusKey(b)
	if string(ka) != string(kb) || aa != ab {
		t.Fatal("a running clock alone isn't a change")
	}
	b.Working = false
	if kb, _ := statusKey(b); string(ka) == string(kb) {
		t.Fatal("working → not is a change")
	}
}

func TestTickSlowsWhenIdle(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	if tickFor(s) != idleTick {
		t.Fatal("nothing started: slow ticks")
	}
	begin(s, pending(s), wed)
	if tickFor(s) != busyTick {
		t.Fatal("a started task: fast ticks")
	}
}
