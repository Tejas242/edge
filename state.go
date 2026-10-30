package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// now is swapped in tests.
var now = time.Now

type Task struct {
	ID         string         `json:"id"`   // "cf:1899A", "lc:sum-game" or "ac:abc480_d"
	Kind       string         `json:"kind"` // new | resolve | reentry | upsolve
	Title      string         `json:"title"`
	URL        string         `json:"url"`
	Rating     int            `json:"rating"`
	Tags       []string       `json:"tags"`
	Focus      string         `json:"focus,omitempty"` // the tag this task was picked for; "" = any topic
	Policy     string         `json:"policy"`          // hint policy: ladder | instant | cold ("" for new until started)
	Arm        string         `json:"arm"`             // experiment arm this result counts toward
	Assigned   time.Time      `json:"assigned"`
	Started    time.Time      `json:"started"` // zero while assigned but not begun
	Finished   time.Time      `json:"finished,omitempty"`
	Outcome    string         `json:"outcome,omitempty"` // solo | assisted | gaveup | skipped | ignored
	Known      bool           `json:"known,omitempty"`   // solved too fast to be new: probably seen before
	Hints      int            `json:"hints"`
	Explain    int            `json:"explain"` // explain-back score 0..2, -1 = not done
	ProbeOf    string         `json:"probe_of,omitempty"`
	ProbeStage int            `json:"probe_stage,omitempty"`
	ProbeTry   int            `json:"probe_try,omitempty"`  // 0 = first attempt at this stage
	Fails      int            `json:"fails"`                // rejected submissions seen
	WorkSec    int            `json:"work_sec"`             // seconds of real work seen (problem, editor or terminal in focus)
	WorkDays   map[string]int `json:"work_days,omitempty"`  // WorkSec by edge day ("2026-10-30"); add with addWork
	WorkAt     time.Time      `json:"work_at,omitempty"`    // when work was last counted: working now, or paused
	DriftSec   int            `json:"drift_sec"`            // seconds on distractions while active
	Edited     bool           `json:"edited,omitempty"`     // the solution file changed after starting
	Offers     int            `json:"offers,omitempty"`     // times offered without being started
	SubSeen    int64          `json:"sub_seen"`             // newest submission time already processed
	Round      string         `json:"round,omitempty"`      // upsolves: the round it's from
	RoundName  string         `json:"round_name,omitempty"` // and its short name

	Judging string `json:"-"`                 // runtime only: "" | "queue" | "test 9" while a submission is judged
	Verdict string `json:"verdict,omitempty"` // latest rejected verdict with its test: "WA on test 3"
}

func (t *Task) Source() string {
	if len(t.ID) < 2 {
		return ""
	}
	return t.ID[:2]
}

func (t *Task) Active() bool { return !t.Started.IsZero() }

// Worked is the real work time seen on the task, not wall-clock: suspend,
// lunch or another app don't count.
func (t *Task) Worked() time.Duration { return time.Duration(t.WorkSec) * time.Second }

// Probe is a scheduled cold re-solve of a problem already done once.
type Probe struct {
	Problem string    `json:"problem"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Rating  int       `json:"rating"`
	Due     time.Time `json:"due"`
	Stage   int       `json:"stage"`         // 1 = first re-solve, 2 = the later one
	Try     int       `json:"try,omitempty"` // failed attempts at this stage so far
	Policy  string    `json:"policy"`        // arm of the original attempt, for the experiment
}

// RatingPoint is one rated contest.
type RatingPoint struct {
	At      time.Time `json:"at"`
	Rating  int       `json:"rating"`
	Contest string    `json:"contest"`
	Round   string    `json:"round,omitempty"` // the round's id: cf:2261, ac:abc480, lc:weekly-contest-524
}

// syncLeg tracks one source's background sync, with backoff on failure.
type syncLeg struct {
	OK    time.Time `json:"ok"`    // last success
	Try   time.Time `json:"try"`   // last attempt
	Full  time.Time `json:"full"`  // last full history fetch (CF)
	Fails int       `json:"fails"` // failures in a row
}

// Trouble is something broken that the user should hear about.
type Trouble struct {
	Since time.Time `json:"since"`
	Msg   string    `json:"msg"`
}

type State struct {
	Cons      Consistency              `json:"consistency"` // Trial 1 (consistency.go)
	Ready     Readiness                `json:"readiness"`   // Trial 2 (readiness.go)
	Current   *Task                    `json:"current,omitempty"`
	History   []Task                   `json:"history"`
	Probes    []Probe                  `json:"probes"`
	Seen      map[string]bool          `json:"seen"` // problems known solved or skipped, never offered as new
	Passes    []time.Time              `json:"passes"`
	GateFrom  time.Time                `json:"gate_from"` // enforcement starts here (install day is free)
	Pinned    *Rules                   `json:"pinned,omitempty"`
	Offset    map[string]int           `json:"offset"`     // per-source difficulty offset, adapted from results
	AdaptFrom map[string]time.Time     `json:"adapt_from"` // results before this already moved the band
	Ratings   map[string]int           `json:"ratings"`    // cf, lc
	RatingLog map[string][]RatingPoint `json:"rating_log"` // contest history per source, for charts
	CFDone    map[string]int64         `json:"cf_done"`    // problem -> first accepted unix time
	CFTried   map[string]bool          `json:"cf_tried"`
	Sync      map[string]*syncLeg      `json:"sync"`
	Snooze    time.Time                `json:"snooze"`
	LastAct   string                   `json:"last_act,omitempty"` // last button, to drop double clicks
	LastActAt time.Time                `json:"last_act_at,omitempty"`
	Said      map[string]time.Time     `json:"said"`     // notifications already sent, by key
	DriftAt   time.Time                `json:"drift_at"` // when the current distraction started
	Trouble   map[string]Trouble       `json:"trouble,omitempty"`
	Rounds    []Round                  `json:"rounds,omitempty"` // live contests followed, oldest first
}

// trouble records a problem, keeping the time it started.
func (s *State) trouble(key, msg string) {
	t, ok := s.Trouble[key]
	if !ok {
		t.Since = now()
	}
	t.Msg = msg
	s.Trouble[key] = t
}

func (s *State) fixed(key string) { delete(s.Trouble, key) }

func dataDir() string {
	if d := os.Getenv("EDGE_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "edge")
}

func statePath() string { return filepath.Join(dataDir(), "state.json") }

func load() (*State, error) {
	s, _, err := loadRaw()
	return s, err
}

// loadRaw reads the state and the exact bytes it came from. A file that won't
// parse falls back to the last good backup instead of switching edge off.
func loadRaw() (*State, []byte, error) {
	s := &State{}
	b, err := os.ReadFile(statePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err == nil {
		if perr := json.Unmarshal(b, s); perr != nil {
			bak, berr := os.ReadFile(statePath() + ".bak")
			s = &State{}
			if berr != nil || json.Unmarshal(bak, s) != nil {
				return nil, nil, fmt.Errorf("state.json is corrupt and there's no good backup: %w", perr)
			}
			b = nil // force a save, which repairs state.json
			s.init()
			s.trouble("state", "state.json was corrupt; restored the backup from "+modTime(statePath()+".bak"))
		}
	}
	s.init()
	return s, b, nil
}

func (s *State) init() {
	if s.Seen == nil {
		s.Seen = map[string]bool{}
	}
	if s.Offset == nil {
		s.Offset = map[string]int{}
	}
	if s.Ratings == nil {
		s.Ratings = map[string]int{}
	}
	if s.CFDone == nil {
		s.CFDone = map[string]int64{}
	}
	if s.Said == nil {
		s.Said = map[string]time.Time{}
	}
	if s.CFTried == nil {
		s.CFTried = map[string]bool{}
	}
	if s.Trouble == nil {
		s.Trouble = map[string]Trouble{}
	}
	if s.Sync == nil {
		s.Sync = map[string]*syncLeg{}
	}
	if s.RatingLog == nil {
		s.RatingLog = map[string][]RatingPoint{}
	}
	if s.AdaptFrom == nil {
		s.AdaptFrom = map[string]time.Time{}
	}
}

func modTime(p string) string {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime().Format("Mon 15:04")
	}
	return "?"
}

// update runs f on the state under an exclusive lock: the daemon and Claude's
// commands write it. Keep f free of network calls: the lock is shared.
func update(f func(*State) error) error {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(dataDir(), "state.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	s, raw, err := loadRaw()
	if err != nil {
		return err
	}
	if err := f(s); err != nil {
		return err
	}
	b, err := s.encode()
	if err != nil || bytes.Equal(b, raw) {
		return err // nothing changed: no write
	}
	// keep a backup of the last good state, refreshed at most hourly
	if bak := statePath() + ".bak"; raw != nil {
		if st, err := os.Stat(bak); err != nil || now().Sub(st.ModTime()) > time.Hour {
			writeAtomic(bak, raw, 0o644)
		}
	}
	return writeAtomic(statePath(), b, 0o644)
}

func (s *State) encode() ([]byte, error) {
	return json.MarshalIndent(s, "", " ")
}

func (s *State) save() error {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	b, err := s.encode()
	if err != nil {
		return err
	}
	return writeAtomic(statePath(), b, 0o644)
}

// writeAtomic replaces path with b: unique temp file, fsync, rename, fsync
// the directory. A crash leaves either the old file or the new one.
func writeAtomic(path string, b []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op after the rename
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
