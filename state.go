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
	ID         string    `json:"id"`   // "cf:1899A" or "lc:sum-game"
	Kind       string    `json:"kind"` // new | resolve | reentry
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Rating     int       `json:"rating"`
	Tags       []string  `json:"tags"`
	Focus      string    `json:"focus,omitempty"` // the weak tag this task was picked for
	Policy     string    `json:"policy"`          // hint policy: ladder | instant | cold
	Arm        string    `json:"arm"`             // experiment arm this result counts toward
	Assigned   time.Time `json:"assigned"`
	Started    time.Time `json:"started"` // zero while assigned but not begun
	Finished   time.Time `json:"finished,omitempty"`
	Outcome    string    `json:"outcome,omitempty"` // solo | assisted | gaveup | skipped
	Hints      int       `json:"hints"`
	Explain    int       `json:"explain"` // explain-back score 0..2, -1 = not done
	ProbeOf    string    `json:"probe_of,omitempty"`
	ProbeStage int       `json:"probe_stage,omitempty"`
	Fails      int       `json:"fails"`     // rejected submissions seen
	DriftMin   int       `json:"drift_min"` // minutes on distractions while active
	SubSeen    int64     `json:"sub_seen"`  // newest submission time already processed
}

func (t *Task) Source() string { return t.ID[:2] }

func (t *Task) Active() bool { return !t.Started.IsZero() }

// Probe is a scheduled cold re-solve of a problem already done once.
type Probe struct {
	Problem string    `json:"problem"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Rating  int       `json:"rating"`
	Due     time.Time `json:"due"`
	Stage   int       `json:"stage"`  // 1 = +3d, 2 = +10d, 3 = +14d solo check
	Policy  string    `json:"policy"` // arm of the original attempt, for the experiment
}

type State struct {
	Current  *Task                `json:"current,omitempty"`
	History  []Task               `json:"history"`
	Probes   []Probe              `json:"probes"`
	Seen     map[string]bool      `json:"seen"` // problems known solved or skipped, never offered as new
	Passes   []time.Time          `json:"passes"`
	GateRuns int                  `json:"gate_runs"`
	GateLast time.Time            `json:"gate_last"`
	GateOff  []time.Time          `json:"gate_off"`  // times status found the hook missing
	GateFrom time.Time            `json:"gate_from"` // enforcement starts here (install day is free)
	Offset   map[string]int       `json:"offset"`    // per-source difficulty offset, adapted from results
	Ratings  map[string]int       `json:"ratings"`   // cf, lc
	CFDone   map[string]int64     `json:"cf_done"`   // problem -> first accepted unix time
	CFTried  map[string]bool      `json:"cf_tried"`
	LastSync time.Time            `json:"last_sync"`
	LastPoll time.Time            `json:"last_poll"`
	Snooze   time.Time            `json:"snooze"`
	Said     map[string]time.Time `json:"said"`              // notifications already sent, by key
	DriftAt  time.Time            `json:"drift_at"`          // when the current distraction started
	Trouble  map[string]string    `json:"trouble,omitempty"` // what's broken right now, said once a day
}

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
			if s.Trouble == nil {
				s.Trouble = map[string]string{}
			}
			s.Trouble["state"] = "state.json was corrupt; restored the backup from " + modTime(statePath()+".bak")
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
		s.Trouble = map[string]string{}
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
