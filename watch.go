package main

import (
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
	"syscall"
	"time"
	"unsafe"
)

// The detection core: what the screen and the workspace say, as events.
// One long-lived `niri msg -j event-stream` keeps the focused window (exec
// per tick only while the stream is down), inotify on the workspace sees
// solution files created and saved, and a terminal's cwd is read only when
// it gains focus (cached per window for a minute). Work time is integrated
// from these events on the monotonic clock, so a tick stalled on the
// network or a suspend neither loses nor invents work.

const (
	cwdFresh    = time.Minute      // a terminal's cwd is re-read at most this often
	burstEvery  = 3 * time.Second  // judge burst cadence after a submit signal
	burstMax    = 40               // polls in one burst (2 min)
	burstGrace  = 3                // polls a burst makes before giving up on finding a pending submission
	trickleFor  = 10 * time.Minute // the judge is polled only this long after work was seen
	workingFor  = 40 * time.Second // workState's "working": work counted this recently
	pausedAfter = 2 * time.Hour    // started, no work for this long: shown paused
	flipDelay   = 3 * time.Second  // debounce before pushing a working↔paused change
)

// senseTask is what the sensor needs to know about the current task.
type senseTask struct {
	ID, Title string
	Count     bool   // work counts now: started, and no live round holds the time
	Path      string // the solution file
	WS        string // the practice workspace
}

type fileMark struct {
	at   time.Time
	seen bool // from an event (or the first stat): false = the file doesn't exist
}

type cwdMark struct {
	at time.Time
	in bool // a program under the terminal runs inside the workspace
}

// sensor is the in-process detection core. Its zero value (and a nil one)
// works in fallback mode: exec niri and stat files when asked.
type sensor struct {
	mu gosync.Mutex

	wins     map[uint64]window
	focus    uint64
	streamUp bool

	root    string // the watched workspace; "" = inotify is off
	inoUp   bool
	files   map[string]fileMark
	freshAt *time.Timer // re-evaluates when a file edit stops being fresh

	cwd map[uint64]cwdMark

	task     senseTask
	on       bool      // working at the last evaluation
	since    time.Time // the last evaluation (monotonic)
	acc      time.Duration
	lastWork time.Time // wall clock: when work was last counted
	lastWin  window

	submitAt time.Time // the last judge-tab title change while the task counts

	wake  chan struct{}    // something the daemon should act on (cap 1)
	clock func() time.Time // monotonic; swapped in tests
}

func newSensor() *sensor {
	return &sensor{wins: map[uint64]window{}, files: map[string]fileMark{}, cwd: map[uint64]cwdMark{}, wake: make(chan struct{}, 1), clock: time.Now}
}

// sense is the daemon's sensor; nil outside the daemon.
var sense *sensor

// start runs the niri stream and the workspace watch in the background.
func (x *sensor) start(root string) {
	go x.runNiri()
	go x.runInotify(root)
}

func (x *sensor) poke() {
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

// ---- niri ----

type niriWin struct {
	ID      uint64 `json:"id"`
	Title   string `json:"title"`
	AppID   string `json:"app_id"`
	PID     int    `json:"pid"`
	Focused bool   `json:"is_focused"`
}

func (w niriWin) window() window { return window{ID: w.ID, App: w.AppID, Title: w.Title, PID: w.PID} }

type niriEvent struct {
	WindowsChanged *struct {
		Windows []niriWin `json:"windows"`
	} `json:"WindowsChanged"`
	WindowOpenedOrChanged *struct {
		Window niriWin `json:"window"`
	} `json:"WindowOpenedOrChanged"`
	WindowClosed *struct {
		ID uint64 `json:"id"`
	} `json:"WindowClosed"`
	WindowFocusChanged *struct {
		ID *uint64 `json:"id"`
	} `json:"WindowFocusChanged"`
}

// runNiri keeps the event stream up, restarting it with backoff. While it's
// down, window() falls back to exec.
func (x *sensor) runNiri() {
	if _, err := exec.LookPath("niri"); err != nil || os.Getenv("NIRI_SOCKET") == "" {
		return // not on niri: exec fallback (which finds nothing) it is
	}
	fails := 0
	for {
		began := time.Now()
		err := x.niriStream()
		x.mu.Lock()
		x.streamUp = false
		x.mu.Unlock()
		if time.Since(began) > 5*time.Minute {
			fails = 0
		}
		fails++
		wait := min(5*time.Minute, time.Second<<min(fails-1, 8))
		log.Printf("niri event stream ended (%v); restarting in %s", err, wait)
		time.Sleep(wait)
	}
}

func (x *sensor) niriStream() error {
	cmd := exec.Command("niri", "msg", "-j", "event-stream")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	dec := json.NewDecoder(out)
	for {
		var ev niriEvent
		if err := dec.Decode(&ev); err != nil {
			return err
		}
		x.niri(ev)
	}
}

// niri applies one stream event; a change to the focused window re-evaluates.
func (x *sensor) niri(ev niriEvent) {
	x.mu.Lock()
	defer x.mu.Unlock()
	before := x.focusedLocked()
	switch {
	case ev.WindowsChanged != nil:
		clear(x.wins)
		x.focus = 0
		for _, w := range ev.WindowsChanged.Windows {
			x.wins[w.ID] = w.window()
			if w.Focused {
				x.focus = w.ID
			}
		}
		x.streamUp = true
	case ev.WindowOpenedOrChanged != nil:
		w := ev.WindowOpenedOrChanged.Window
		x.wins[w.ID] = w.window()
		if w.Focused {
			x.focus = w.ID
		} else if x.focus == w.ID {
			x.focus = 0
		}
	case ev.WindowClosed != nil:
		delete(x.wins, ev.WindowClosed.ID)
		delete(x.cwd, ev.WindowClosed.ID)
		if x.focus == ev.WindowClosed.ID {
			x.focus = 0
		}
	case ev.WindowFocusChanged != nil:
		x.focus = 0
		if id := ev.WindowFocusChanged.ID; id != nil {
			x.focus = *id
		}
	default:
		return
	}
	if !x.streamUp {
		return
	}
	after := x.focusedLocked()
	if after == before {
		return // another window changed: nothing to judge
	}
	if after.ID == before.ID && after.Title != before.Title && x.task.Count && judgeTab(after, x.task) {
		x.submitAt = x.clock() // the judge tab moved on: maybe a submit
		x.poke()
	}
	x.evalLocked(x.clock(), after)
}

func (x *sensor) focusedLocked() window {
	if x.focus == 0 {
		return window{}
	}
	return x.wins[x.focus]
}

// window is the focused window: from the stream, or one exec while it's down.
func (x *sensor) window() window {
	if x == nil {
		return focused()
	}
	x.mu.Lock()
	if x.streamUp {
		w := x.focusedLocked()
		x.mu.Unlock()
		return w
	}
	x.mu.Unlock()
	return focused()
}

// ---- inotify ----

const inoMask = syscall.IN_CREATE | syscall.IN_MOVED_TO | syscall.IN_CLOSE_WRITE | syscall.IN_DELETE | syscall.IN_MOVED_FROM | syscall.IN_DELETE_SELF

// runInotify watches every directory under root (skipping hidden ones) for
// created, saved, moved and deleted files. New directories are added as
// they appear (competitest makes a contest's dir and its files together).
func (x *sensor) runInotify(root string) {
	if root == "" {
		return
	}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		log.Println("inotify:", err)
		return
	}
	defer syscall.Close(fd)
	dirs := map[int32]string{}
	add := func(top string, fresh bool) {
		filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				if fresh {
					x.fileEvent(p, true) // made in a new dir before its watch was up
				}
				return nil
			}
			if p != root && strings.HasPrefix(d.Name(), ".") || len(dirs) >= 4096 {
				return filepath.SkipDir
			}
			if wd, err := syscall.InotifyAddWatch(fd, p, inoMask); err == nil {
				dirs[int32(wd)] = p
			}
			return nil
		})
	}
	os.MkdirAll(root, 0o755)
	add(root, false)
	if len(dirs) == 0 {
		log.Println("inotify: can't watch", root)
		return
	}
	x.mu.Lock()
	x.root, x.inoUp = root, true
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		x.inoUp = false
		x.mu.Unlock()
	}()
	buf := make([]byte, 64<<10)
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			log.Println("inotify read:", err)
			return
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := ""
			if ev.Len > 0 {
				b := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
				if i := slices.Index(b, 0); i >= 0 {
					b = b[:i]
				}
				name = string(b)
			}
			off += syscall.SizeofInotifyEvent + int(ev.Len)
			dir, ok := dirs[ev.Wd]
			switch {
			case ev.Mask&syscall.IN_Q_OVERFLOW != 0:
				x.mu.Lock()
				clear(x.files) // events were lost: stat afresh
				x.mu.Unlock()
			case ev.Mask&syscall.IN_IGNORED != 0:
				delete(dirs, ev.Wd)
			case !ok || name == "":
			case ev.Mask&syscall.IN_ISDIR != 0:
				if ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 && !strings.HasPrefix(name, ".") {
					add(filepath.Join(dir, name), true)
				}
			default:
				x.fileEvent(filepath.Join(dir, name), ev.Mask&(syscall.IN_DELETE|syscall.IN_MOVED_FROM) == 0)
			}
		}
		if len(dirs) == 0 { // the workspace itself is gone: nothing left to watch
			log.Println("inotify: workspace gone")
			return
		}
	}
}

// fileEvent records a file made or saved (or gone) now.
func (x *sensor) fileEvent(p string, present bool) {
	n := now()
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.files) > 512 {
		for k, m := range x.files {
			if n.Sub(m.at) > time.Hour {
				delete(x.files, k)
			}
		}
	}
	x.files[p] = fileMark{at: n, seen: present}
	if x.task.Path == "" || !sameStem(p, x.task.Path) {
		return
	}
	if present && x.task.Count {
		if x.freshAt == nil {
			x.freshAt = time.AfterFunc(editFresh, x.reeval)
		} else {
			x.freshAt.Reset(editFresh)
		}
	}
	if !x.task.Count && p == x.task.Path && present {
		x.poke() // the problem arrived: the daemon begins the task
	}
	x.evalLocked(x.clock(), x.curWinLocked())
}

// sameStem: p is the solution file or one of its build products (C.cpp, C, C.out).
func sameStem(p, sol string) bool {
	if filepath.Dir(p) != filepath.Dir(sol) {
		return false
	}
	stem := strings.TrimSuffix(filepath.Base(sol), filepath.Ext(sol))
	b := filepath.Base(p)
	return b == stem || strings.TrimSuffix(b, filepath.Ext(b)) == stem
}

func (x *sensor) reeval() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.evalLocked(x.clock(), x.curWinLocked())
}

// curWinLocked is the focused window as last known: live from the stream, or
// the last exec while it's down.
func (x *sensor) curWinLocked() window {
	if x.streamUp {
		return x.focusedLocked()
	}
	return x.lastWin
}

// fileTime is when p was last made or saved, and whether it exists. With the
// watch up it's answered from events (one stat the first time p is asked
// about); otherwise by a stat.
func (x *sensor) fileTime(p string) (time.Time, bool) {
	if x != nil {
		x.mu.Lock()
		defer x.mu.Unlock()
		return x.fileTimeLocked(p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

func (x *sensor) fileTimeLocked(p string) (time.Time, bool) {
	watched := x.inoUp && x.root != "" && strings.HasPrefix(p, x.root+string(filepath.Separator))
	if watched {
		if m, ok := x.files[p]; ok {
			return m.at, m.seen
		}
	}
	st, err := os.Stat(p)
	if watched {
		m := fileMark{}
		if err == nil {
			m = fileMark{at: st.ModTime(), seen: true}
		}
		x.files[p] = m
	}
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

// lastEditLocked is the newest event on the solution or its build products.
func (x *sensor) lastEditLocked(sol string) time.Time {
	at, _ := x.fileTimeLocked(sol)
	if x.inoUp {
		for p, m := range x.files {
			if m.seen && m.at.After(at) && sameStem(p, sol) {
				at = m.at
			}
		}
	}
	return at
}

// ---- work ----

// evalLocked integrates work up to t and decides whether w (the focused
// window) means work now.
func (x *sensor) evalLocked(t time.Time, w window) {
	if x.on && !x.since.IsZero() {
		x.acc += t.Sub(x.since) // monotonic: a suspend adds nothing
		x.lastWork = now()
	}
	x.since = t
	x.lastWin = w
	on := false
	if x.task.Count {
		fresh := false
		if e := x.lastEditLocked(x.task.Path); !e.IsZero() {
			fresh = now().Sub(e) < editFresh
		}
		on = fresh || x.screenWorkLocked(w)
	}
	if on {
		x.lastWork = now()
	}
	if on != x.on {
		x.on = on
		x.poke()
	}
}

// screenWorkLocked is working() for the focused window, with the terminal's
// cwd read once per focus (and at most once a minute).
func (x *sensor) screenWorkLocked(w window) bool {
	if !isTermApp(w.App) {
		return judgeTab(w, x.task)
	}
	if w.PID <= 0 {
		return false
	}
	c, ok := x.cwd[w.ID]
	if !ok || x.clock().Sub(c.at) >= cwdFresh {
		c = cwdMark{at: x.clock(), in: cwdsUnder(procCwds(w.PID), x.task.WS)}
		x.cwd[w.ID] = c
	}
	return c.in
}

// take hands the daemon the work counted for st since the last take, and
// tells the sensor what to watch next. A different task drops what was
// counted for the old one.
func (x *sensor) take(st senseTask, fallback window) (sec int, lastWork time.Time, on bool) {
	if x == nil {
		return 0, time.Time{}, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	t := x.clock()
	w := fallback // the stream is down: the tick's one exec
	if x.streamUp {
		w = x.focusedLocked()
	}
	if x.task.ID != st.ID {
		x.evalLocked(t, w) // close the old task's span
		x.acc, x.lastWork, x.on = 0, time.Time{}, false
		if x.freshAt != nil {
			x.freshAt.Stop()
		}
	}
	x.task = st
	x.evalLocked(t, w)
	if !st.Count {
		x.acc = 0 // unstarted, or a live round holds the time
	}
	sec = int(x.acc / time.Second)
	x.acc -= time.Duration(sec) * time.Second
	return sec, x.lastWork, x.on
}

// submitted is the last judge-tab title change seen.
func (x *sensor) submitted() time.Time {
	if x == nil {
		return time.Time{}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.submitAt
}

// workingNow is the sensor's live answer for task id; ok false outside the
// daemon or for another task.
func (x *sensor) workingNow(id string) (on, ok bool) {
	if x == nil {
		return false, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.task.ID != id || id == "" {
		return false, false
	}
	return x.on, true
}
