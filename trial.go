package main

import "time"

// The v2 trials, pre-registered (see the edge v2 plan): a clean baseline week
// after the fixes, then Trial 1 (the consistency loop), then Trial 2 (the
// readiness engine). Each switches on by itself on its date; v2/consistency
// makes the dates configurable and adds the revert rule.
var (
	trial1From = time.Date(2026, 11, 6, 0, 0, 0, 0, time.Local)
	trial2From = time.Date(2026, 11, 20, 0, 0, 0, 0, time.Local)
)

// phase is the trial phase on day t: baseline | trial1 | trial2.
func (s *State) phase(t time.Time) string {
	d := day(t)
	switch {
	case !d.Before(day(trial2From.Add(12 * time.Hour))):
		return "trial2"
	case !d.Before(day(trial1From.Add(12 * time.Hour))):
		return "trial1"
	}
	return "baseline"
}
