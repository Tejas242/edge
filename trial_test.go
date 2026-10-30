package main

import (
	"strings"
	"testing"
	"time"
)

// The trial dates and cue times come from [trials]; the default config
// documents every key.
func TestTrialsConfig(t *testing.T) {
	c, bad := parseConfigText(t, defaultConfigText)
	if len(bad) > 0 {
		t.Fatal(bad)
	}
	if !c.Trial1.Equal(trial1From) || !c.Trial2.Equal(trial2From) || c.CueWeekday != 19*time.Hour+30*time.Minute ||
		c.CueWeekend != 12*time.Hour+30*time.Minute || c.OfficeUntil != 17*time.Hour || c.SkipTokens != 1 {
		t.Fatalf("defaults: %+v", c)
	}
	c, bad = parseConfigText(t, "[trials]\ntrial1 = \"2026-12-01\"\ntrial2 = \"2026-12-15\"\ncue_weekday = \"20:15\"\nskip_tokens = 2\n")
	if len(bad) > 0 || c.Trial1.Day() != 1 || c.Trial2.Day() != 15 || c.CueWeekday != 20*time.Hour+15*time.Minute || c.SkipTokens != 2 {
		t.Fatalf("set: %v %+v", bad, c)
	}
	c, bad = parseConfigText(t, "[trials]\ntrial1 = \"2026-12-15\"\ntrial2 = \"2026-12-01\"\ncue_weekend = \"25:00\"\n")
	if len(bad) != 2 || !c.Trial1.Equal(trial1From) || c.CueWeekend != 12*time.Hour+30*time.Minute {
		t.Fatalf("bad values must keep the defaults: %v %+v", bad, c)
	}
}

func parseConfigText(t *testing.T, text string) (*Config, []string) {
	t.Helper()
	writeConfig(t, text)
	return parseConfig(configPath())
}

// The phase follows the config's dates on edge days (04:00 boundaries), and a
// reverted Trial 1 reads as baseline from then on: Trial 2 never starts.
func TestPhaseFollowsConfigAndRevert(t *testing.T) {
	s := fresh(t)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 11, 5, 23, 0, 0, 0, time.Local), "baseline"},
		{time.Date(2026, 11, 6, 3, 30, 0, 0, time.Local), "baseline"}, // still Thursday's edge day
		{time.Date(2026, 11, 6, 4, 0, 0, 0, time.Local), "trial1"},
		{time.Date(2026, 11, 19, 23, 0, 0, 0, time.Local), "trial1"},
		{time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local), "trial2"},
	} {
		if got := s.phase(c.at); got != c.want {
			t.Errorf("%s: %s, want %s", c.at.Format("Mon 2 Jan 15:04"), got, c.want)
		}
	}
	if trial1Days() != 14 || revertLine(14) != 7 {
		t.Fatalf("window %d days, line %d", trial1Days(), revertLine(14))
	}
	writeConfig(t, "[trials]\ntrial1 = \"2026-10-01\"\ntrial2 = \"2026-10-15\"\n")
	if got := s.phase(wed); got != "trial1" {
		t.Fatalf("moved dates: %s", got)
	}
	s.Cons.Reverted = true
	if got := s.phase(time.Date(2026, 10, 20, 20, 0, 0, 0, time.Local)); got != "baseline" || s.consOn(wed) {
		t.Fatalf("reverted: %s", got)
	}
	if !strings.Contains(defaultConfigText, "[trials]") {
		t.Fatal("edge config init must document [trials]")
	}
}
