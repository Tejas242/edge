package main

import "time"

// The v2 trials, pre-registered (see the edge v2 plan): a clean baseline week
// after the fixes, then Trial 1 (the consistency loop), then Trial 2 (the
// readiness engine). Each switches on by itself on its date ([trials] in the
// config moves them). Trial 1 that ends under its line is reverted: its rules
// switch off, phase reads baseline again and Trial 2 never starts.
var (
	trial1From = time.Date(2026, 11, 6, 0, 0, 0, 0, time.Local)
	trial2From = time.Date(2026, 11, 20, 0, 0, 0, 0, time.Local)
)

// The pre-registered lines for Trial 1, out of its days (14 by default):
// success at ≥ 10 reps, revert under 7.
const (
	trial1Success = 10
	trial1Revert  = 7
)

// phase is the trial phase on day t: baseline | trial1 | trial2.
func (s *State) phase(t time.Time) string {
	d := day(t)
	c := cfg()
	switch {
	case s.Cons.Reverted && !d.Before(trialDay(c.Trial1)):
		return "baseline"
	case !d.Before(trialDay(c.Trial2)):
		return "trial2"
	case !d.Before(trialDay(c.Trial1)):
		return "trial1"
	}
	return "baseline"
}

// trialDay is the edge day a trial date names (the date's midday is safely
// inside it: its midnight falls before day_ends, in the day before).
func trialDay(date time.Time) time.Time { return day(date.Add(12 * time.Hour)) }

// consOn: Trial 1's rules hold on day t (trial1, and trial2 on top of it).
func (s *State) consOn(t time.Time) bool { return s.phase(t) != "baseline" }

// trial1Days is the length of Trial 1's window in days.
func trial1Days() int {
	c := cfg()
	return int(trialDay(c.Trial2).Sub(trialDay(c.Trial1)).Hours()/24 + 0.5)
}

// revertLine scales the pre-registered "under 7 of 14" to the window.
func revertLine(days int) int { return (trial1Revert*days + 13) / 14 }
