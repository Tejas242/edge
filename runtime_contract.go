package main

// workState says whether work on t is being counted right now, and whether a
// started task should show as paused (no work for a while, or across the day
// boundary). fix/runtime implements it from its event core; fix/core only
// calls it, to fill the status.
func workState(s *State, t *Task) (working, paused bool) { return false, false }
