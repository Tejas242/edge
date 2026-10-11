package main

import (
	"encoding/json"
	"errors"
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
	Said     map[string]time.Time `json:"said"`     // notifications already sent, by key
	DriftAt  time.Time            `json:"drift_at"` // when the current distraction started
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
	s := &State{}
	b, err := os.ReadFile(statePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, s); err != nil {
			return nil, err
		}
	}
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
	return s, nil
}

// update runs f on the state under an exclusive lock: the daemon, the gate
// hook and Claude's commands all write it.
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
	s, err := load()
	if err != nil {
		return err
	}
	if err := f(s); err != nil {
		return err
	}
	return s.save()
}

func (s *State) save() error {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statePath())
}
