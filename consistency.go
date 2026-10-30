package main

// Consistency is the Trial 1 state: tonight's cue, reps, the chain and skip
// tokens. v2/consistency owns this file.
type Consistency struct{}

// ConsistencyStats is its part of `edge stats`.
type ConsistencyStats struct{}

func (s *State) consistencyStats() *ConsistencyStats { return nil }

// consistencyStatus fills the status fields Trial 1 owns.
func consistencyStatus(s *State, st *emberStatus) {}

// consistencyAction handles Trial 1's action keys (cue:…, skiptonight …);
// handled=false leaves the key to others.
func consistencyAction(s *State, verb, arg string) (handled bool, msg string, err error) {
	return false, "", nil
}
