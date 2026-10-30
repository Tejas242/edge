package main

import "time"

// Readiness is the Trial 2 model: per-pattern skill and memory, the drill and
// the mistake log. v2/readiness owns this file.
type Readiness struct{}

// ReadinessStats is its part of `edge stats`.
type ReadinessStats struct{}

func (s *State) readinessStats() *ReadinessStats { return nil }

// readinessStatus fills the status fields Trial 2 owns.
func readinessStatus(s *State, st *emberStatus) {}

// readinessAction handles Trial 2's action keys (mistake:…, drill …);
// handled=false leaves the key to others.
func readinessAction(s *State, verb, arg string) (handled bool, msg string, err error) {
	return false, "", nil
}

// readinessEvents are this trial's cards for plan(): it checks its own phase.
func readinessEvents(s *State, w window, n time.Time) []event { return nil }
