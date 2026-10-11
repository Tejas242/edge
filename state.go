package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished,omitempty"`
	Outcome    string    `json:"outcome,omitempty"` // solo | assisted | gaveup | skipped
	Hints      int       `json:"hints"`
	Explain    int       `json:"explain"` // explain-back score 0..2, -1 = not done
	ProbeOf    string    `json:"probe_of,omitempty"`
	ProbeStage int       `json:"probe_stage,omitempty"`
}

func (t *Task) Source() string { return t.ID[:2] }

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
	Current  *Task            `json:"current,omitempty"`
	History  []Task           `json:"history"`
	Probes   []Probe          `json:"probes"`
	Seen     map[string]bool  `json:"seen"` // problems known solved or skipped, never offered as new
	Passes   []time.Time      `json:"passes"`
	GateRuns int              `json:"gate_runs"`
	GateLast time.Time        `json:"gate_last"`
	GateOff  []time.Time      `json:"gate_off"`  // times status found the hook missing
	GateFrom time.Time        `json:"gate_from"` // enforcement starts here (install day is free)
	Offset   map[string]int   `json:"offset"`    // per-source difficulty offset, adapted from results
	Ratings  map[string]int   `json:"ratings"`   // cf, lc
	CFDone   map[string]int64 `json:"cf_done"`   // problem -> first accepted unix time
	CFTried  map[string]bool  `json:"cf_tried"`
	LastSync time.Time        `json:"last_sync"`
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
	if s.CFTried == nil {
		s.CFTried = map[string]bool{}
	}
	return s, nil
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
