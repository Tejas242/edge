package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nov is a time in November 2026, Trial 1's month: Fri 6 is its first day,
// Mon 9 the next week, Fri 20 Trial 2's first day.
func nov(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.Local) }

// worked adds a finished task with min focused minutes on its day.
func worked(s *State, id string, d, min int, outcome string) *Task {
	start := nov(d, 20, 0)
	s.History = append(s.History, Task{ID: id, Kind: "new", Title: id, Outcome: outcome, Started: start,
		Finished: start.Add(time.Duration(min) * time.Minute), WorkSec: min * 60,
		WorkDays: map[string]int{dateKey(day(start)): min * 60}})
	return &s.History[len(s.History)-1]
}

// rep gives day d a rep: an accepted solution.
func rep(s *State, d int) { worked(s, "cf:"+strings.Repeat("A", d), d, 30, "solo") }

// consState is a fresh state with Trial 1 on, at t.
func consState(t *testing.T, at0 time.Time) *State {
	t.Helper()
	at(t, at0)
	return fresh(t)
}

// What makes a day's rep: a submit, 25 focused minutes, an AC, a round sat,
// or a give-up after 25 minutes. In the trial phases today() is that rep,
// one a day, weekends included; the baseline keeps its quota.
func TestRepRules(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(s *State)
		kind string
	}{
		{"nothing", func(s *State) {}, ""},
		{"24 focused minutes", func(s *State) { worked(s, "cf:1A", 9, 24, "skipped") }, ""},
		{"25 focused minutes", func(s *State) { worked(s, "cf:1A", 9, 25, "skipped") }, "focus"},
		{"two tasks, 25 minutes", func(s *State) { worked(s, "cf:1A", 9, 9, "skipped"); worked(s, "cf:1B", 9, 16, "ignored") }, "focus"},
		{"a submit", func(s *State) { pending(s).SubSeen = nov(9, 20, 10).Unix() }, "submit"},
		{"an AC", func(s *State) { worked(s, "cf:1A", 9, 12, "solo") }, "ac"},
		{"a give-up after 30", func(s *State) { worked(s, "cf:1A", 9, 30, "gaveup") }, "giveup"},
		{"a give-up after 10", func(s *State) { worked(s, "cf:1A", 9, 10, "gaveup") }, ""},
		{"a round", func(s *State) {
			s.Rounds = append(s.Rounds, Round{ID: "cf:2272", Start: nov(9, 20, 5), DurSec: 7200, Joined: "live"})
		}, "round"},
		{"yesterday's AC", func(s *State) { worked(s, "cf:1A", 8, 30, "solo") }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := consState(t, nov(9, 23, 0))
			c.set(s)
			cs := s.chain()
			if cs.Kind != c.kind {
				t.Fatalf("kind %q, want %q", cs.Kind, c.kind)
			}
			got, need := s.today()
			if want := map[bool]int{true: 1, false: 0}[c.kind != ""]; got != want || need != 1 {
				t.Fatalf("today %d/%d", got, need)
			}
		})
	}
	s := consState(t, nov(7, 15, 0)) // Saturday: one rep too
	if got, need := s.today(); got != 0 || need != 1 || s.dailyMin(now()) != 1 {
		t.Fatalf("weekend: %d/%d", got, need)
	}
	s = consState(t, wed) // baseline: the quota as before
	worked(s, "cf:1A", 0, 30, "skipped")
	if got, need := s.today(); got != 0 || need != 1 {
		t.Fatalf("baseline: %d/%d", got, need)
	}
}

// The gate reads the rep: closed in study time until it counts.
func TestGateHoldsToTheRep(t *testing.T) {
	s := consState(t, nov(9, 21, 0))
	h, _ := os.UserHomeDir()
	other := filepath.Join(h, "Gems/kinvault")
	if v := gateDecide(s, "fix the bug", other); !v.Block {
		t.Fatal("closed before the rep")
	}
	pending(s)
	begin(s, s.Current, nov(9, 20, 30))
	s.Current.addWork(nov(9, 20, 55), 25*60)
	if v := gateDecide(s, "fix the bug", other); v.Block {
		t.Fatalf("25 focused minutes open it: %s", v.Reason)
	}
}

// The chain: rep days in a row, one miss forgiven, two in a row break it,
// a token day neutral, today pending until its rep. Never "0" on screen:
// after a break the next rep is 1.
func TestChain(t *testing.T) {
	for _, c := range []struct {
		name          string
		reps, tokens  []int
		today         int
		chain, missed int
	}{
		{"three in a row, today pending", []int{6, 7, 8}, nil, 9, 3, 0},
		{"one miss is forgiven", []int{6, 7, 9}, nil, 10, 3, 0},
		{"yesterday missed", []int{6, 7, 8}, nil, 10, 3, 1},
		{"two misses break it", []int{6, 7}, nil, 10, 0, 0},
		{"an old break", []int{3, 6, 7}, nil, 9, 2, 1},
		{"a token is neutral", []int{6, 8}, []int{7}, 9, 2, 0},
		{"a token doesn't hide a second miss", []int{6}, []int{8}, 10, 0, 0},
		{"nothing yet", nil, nil, 9, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := consState(t, nov(c.today, 21, 0))
			for _, d := range c.reps {
				rep(s, d)
			}
			for _, d := range c.tokens {
				s.Cons.Tokens = append(s.Cons.Tokens, dateKey(nov(d, 12, 0)))
			}
			cs := s.chain()
			if cs.Chain != c.chain || cs.Missed != c.missed || cs.RepDone {
				t.Fatalf("chain %d missed %d done %v, want %d %d", cs.Chain, cs.Missed, cs.RepDone, c.chain, c.missed)
			}
			if s.streak() != c.chain {
				t.Fatalf("streak %d is the chain in the trial", s.streak())
			}
			rep(s, c.today)
			if cs := s.chain(); cs.Chain != c.chain+1 || !cs.RepDone || cs.Missed != c.missed {
				t.Fatalf("after today's rep: %+v", cs)
			}
		})
	}
}

// Never miss twice: the day after a miss, 20 focused minutes count, and the
// daemon latches that rep so it stays a rep tomorrow.
func TestNeverMissTwice(t *testing.T) {
	s := consState(t, nov(10, 21, 0))
	rep(s, 6)
	rep(s, 7)
	rep(s, 8) // 9 missed
	pending(s)
	begin(s, s.Current, nov(10, 20, 30))
	s.Current.addWork(nov(10, 20, 50), 20*60)
	cs := s.chain()
	if !cs.RepDone || cs.Kind != "focus" || cs.Chain != 4 || cs.Missed != 1 {
		t.Fatalf("20 minutes after a miss: %+v", cs)
	}
	consistencyEvents(s, window{}, now())
	if r := s.Cons.rec("2026-11-10"); r == nil || !r.Rep || r.RepFocus != 20 || r.StartAt.IsZero() {
		t.Fatalf("latched: %+v", r)
	}
	at(t, nov(11, 21, 0))
	if cs := s.chain(); cs.Chain != 4 || cs.Missed != 0 {
		t.Fatalf("next day: %+v", cs)
	}
	rep(s, 11)
	at(t, nov(12, 21, 0)) // a normal day: 25 again
	s.Current.addWork(nov(12, 20, 50), 20*60)
	if cs := s.chain(); cs.RepDone {
		t.Fatalf("20 minutes on a normal day: %+v", cs)
	}
}

// The status carries the chain fields only in the trial phases.
func TestConsistencyStatusOnlyInTrials(t *testing.T) {
	s := consState(t, nov(5, 21, 0))
	rep(s, 3)
	rep(s, 4)
	if st := edgeStatus(s); st.Chain != 0 || st.SkipTokens != 0 || st.RepDone || st.CueMs != 0 || st.Phase != "baseline" {
		t.Fatalf("baseline: %+v", st)
	}
	at(t, nov(6, 21, 0))
	rep(s, 5)
	st := edgeStatus(s)
	if st.Phase != "trial1" || st.Chain != 3 || st.SkipTokens != 1 || st.RepDone || st.Today != 0 || st.Need != 1 || st.Streak != 3 {
		t.Fatalf("trial1: %+v", st)
	}
}

// consStep runs one daemon step's planning at t with the focused window w,
// marking what's said as delivered.
func consStep(t *testing.T, s *State, when time.Time, w window) []event {
	t.Helper()
	at(t, when)
	return tell(s, w, 0)
}

func cueKeys(evs []event) string {
	var k []string
	for _, e := range evs {
		if strings.HasPrefix(e.Key, "cue") {
			k = append(k, e.Key)
		}
	}
	return strings.Join(k, " ")
}

func chipKeys(e event) string {
	var k []string
	for _, c := range e.Chips {
		k = append(k, c.Key+"="+c.Label)
	}
	return strings.Join(k, " ")
}

func resetConsRT(t *testing.T) {
	consRT = struct {
		at          time.Time
		win         window
		activeAt    time.Time
		unlockMoved bool
	}{}
	t.Cleanup(func() {
		consRT.at, consRT.win, consRT.activeAt, consRT.unlockMoved = time.Time{}, window{}, time.Time{}, false
	})
}

var (
	termWin    = window{ID: 1, App: "Alacritty", Title: "~"}
	browserWin = window{ID: 2, App: "firefox", Title: "YouTube"}
)

// The "When?" card comes once, at the first unlock after office: the
// laptop opened after a sleep (the wall clock jumped between steps) and the
// screen moved. Chips: Now, the default cue, 90 minutes later, a skip.
func TestCueCardAtFirstUnlockAfterOffice(t *testing.T) {
	s := consState(t, nov(9, 8, 0)) // Monday
	resetConsRT(t)
	pending(s)
	if k := cueKeys(consStep(t, s, nov(9, 8, 0), termWin)); k != "" {
		t.Fatalf("morning: %s", k)
	}
	consStep(t, s, nov(9, 8, 1), browserWin)
	// 08:01 → 18:10: asleep all day; the first step after the resume
	if k := cueKeys(consStep(t, s, nov(9, 18, 10), browserWin)); k != "" {
		t.Fatalf("a resume alone waits for the lock screen to go: %s", k)
	}
	evs := consStep(t, s, nov(9, 18, 10).Add(20*time.Second), termWin)
	if k := cueKeys(evs); k != "cue:2026-11-09" {
		t.Fatalf("unlock: %q", k)
	}
	e := evs[0]
	for _, x := range evs {
		if x.Key == "cue:2026-11-09" {
			e = x
		}
	}
	if e.Title != "Tonight's rep: 2257C" || !strings.HasSuffix(e.Body, "\nWhen?") || !strings.Contains(e.Body, "New week, new chain") {
		t.Fatalf("card: %q / %q", e.Title, e.Body)
	}
	if got := chipKeys(e); got != "cue:now=Now cue:19:30=19:30 cue:21:00=21:00 skiptonight=Skip (1 left)" {
		t.Fatalf("chips: %s", got)
	}
	for _, c := range e.Chips {
		if len([]rune(c.Label)) > 18 {
			t.Fatalf("chip label too long for Ember: %q", c.Label)
		}
	}
	if c := emberCardFor(e); c.ID != "cue" || c.Mood != "work" || len(c.Chips) != 4 || c.TTL == 0 {
		t.Fatalf("ember card: %+v", c)
	}
	// later unlocks the same day: nothing more
	consStep(t, s, nov(9, 18, 12), browserWin)
	if k := cueKeys(consStep(t, s, nov(9, 20, 30), termWin)); k != "" && !strings.HasPrefix(k, "cuenudge") {
		t.Fatalf("a second card: %s", k)
	}
	if r := s.Cons.rec("2026-11-09"); r.UnlockAt.IsZero() || r.CardAt.IsZero() {
		t.Fatalf("record: %+v", r)
	}
}

// An unlock that's already past the default cue: Now leads, one later time.
// Idle at the desk counts like a sleep: 90 minutes without the screen moving.
func TestCueCardPastTheCue(t *testing.T) {
	s := consState(t, nov(10, 18, 0))
	resetConsRT(t)
	s.Cons.SeenAt = nov(10, 17, 59) // in use until a minute ago
	consStep(t, s, nov(10, 18, 0), termWin)
	for m := 1; m <= 140; m++ { // awake, nobody there: the daemon ticks every minute
		if k := cueKeys(consStep(t, s, nov(10, 18, 0).Add(time.Duration(m)*time.Minute), termWin)); k != "" {
			t.Fatalf("idle: %s at %d", k, m)
		}
	}
	evs := consStep(t, s, nov(10, 20, 21), browserWin)
	if len(evs) == 0 || evs[0].Key != "cue:2026-11-10" {
		t.Fatalf("unlock at 20:21: %s", keys(evs))
	}
	if got := chipKeys(evs[0]); got != "cue:now=Now cue:21:00=21:00 skiptonight=Skip (1 left)" {
		t.Fatalf("chips: %s", got)
	}
	if evs[0].Title != "Tonight's rep: one problem" {
		t.Fatalf("no task yet: %q", evs[0].Title)
	}
	// no answer: the card's time is the cue, so 15 minutes on, one nudge with Start
	if st := edgeStatus(s); st.CueMs != nov(10, 20, 21).UnixMilli() {
		t.Fatalf("cue %v", time.UnixMilli(st.CueMs))
	}
	if k := keys(consStep(t, s, nov(10, 20, 35), browserWin)); strings.Contains(k, "cuenudge") {
		t.Fatalf("early nudge: %s", k)
	}
	evs = consStep(t, s, nov(10, 20, 37), termWin)
	if k := cueKeys(evs); k != "cuenudge:2026-11-10" {
		t.Fatalf("nudge: %q", k)
	}
	if a := evs[len(evs)-1].Actions; len(a) != 1 || a[0].Key != "next" || a[0].Label != "Start" {
		t.Fatalf("nudge actions: %+v", a)
	}
	if k := cueKeys(consStep(t, s, nov(10, 21, 30), browserWin)); k != "" {
		t.Fatalf("nothing more that evening: %s", k)
	}
	// weekends: past 10:00 is after office; and late in the evening, "Now" leads
	s = consState(t, nov(14, 9, 0))
	resetConsRT(t)
	consStep(t, s, nov(14, 9, 0), termWin)
	if k := cueKeys(consStep(t, s, nov(14, 9, 1), browserWin)); k != "" {
		t.Fatalf("Saturday 09:01: %s", k)
	}
	if k := cueKeys(consStep(t, s, nov(14, 11, 0), termWin)); k != "cue:2026-11-14" {
		t.Fatalf("Saturday 11:00 after a sleep: %q", k)
	}
}

// After one miss the card says so, and the bar is 20 minutes.
func TestCueCardAfterAMiss(t *testing.T) {
	s := consState(t, nov(11, 18, 0)) // Wed; Tue missed
	resetConsRT(t)
	rep(s, 9)
	pending(s)
	s.Cons.SeenAt = nov(11, 7, 0)
	consStep(t, s, nov(11, 18, 0), termWin)
	evs := consStep(t, s, nov(11, 18, 1), browserWin)
	if len(evs) == 0 || evs[0].Body != "Yesterday slipped. Tonight keeps the chain. A 20-minute rep counts.\nWhen?" {
		t.Fatalf("after a miss: %+v", evs)
	}
}

// Picking a time moves tonight's cue; Now is now. Baseline: no cards at all,
// but the day is recorded.
func TestCueActions(t *testing.T) {
	actionEnv(t)
	s := consState(t, nov(9, 18, 30))
	s.save()
	if _, err := act("cue:21:00"); err != nil {
		t.Fatal(err)
	}
	s, _ = load()
	if st := edgeStatus(s); st.CueMs != nov(9, 21, 0).UnixMilli() {
		t.Fatalf("cue %v", time.UnixMilli(st.CueMs))
	}
	at(t, nov(9, 18, 31))
	if msg, err := act("cue:now"); err != nil || msg != "Tonight's rep: now" {
		t.Fatal(msg, err)
	}
	s, _ = load()
	if r := s.Cons.rec("2026-11-09"); !r.CueSet || !r.CueAt.Equal(nov(9, 18, 31)) {
		t.Fatalf("now: %+v", r)
	}
	at(t, nov(9, 18, 40))
	if _, err := act("cue:18:00"); err != nil { // a stale chip: the past means now
		t.Fatal(err)
	}
	if _, err := act("cue:25:00"); err == nil {
		t.Fatal("bad time accepted")
	}
	at(t, nov(10, 0, 30))
	if _, err := act("cue:01:15"); err != nil {
		t.Fatal(err)
	}
	s, _ = load()
	if r := s.Cons.rec("2026-11-09"); !r.CueAt.Equal(nov(10, 1, 15)) {
		t.Fatalf("small hours belong to the evening: %v", r.CueAt)
	}

	s = consState(t, nov(4, 18, 0)) // baseline
	resetConsRT(t)
	pending(s)
	consStep(t, s, nov(4, 18, 0), termWin)
	consStep(t, s, nov(4, 18, 1), browserWin)
	for _, e := range consStep(t, s, nov(4, 19, 50), termWin) {
		if strings.HasPrefix(e.Key, "cue") || len(e.Chips) > 0 {
			t.Fatalf("baseline card: %s", e.Key)
		}
	}
	if r := s.Cons.rec("2026-11-04"); r == nil || r.Phase != "baseline" || r.UnlockAt.IsZero() {
		t.Fatalf("baseline record: %+v", r)
	}
	if st := edgeStatus(s); st.CueMs != 0 {
		t.Fatal("baseline status carries a cue")
	}
	s.save()
	if _, err := act("skiptonight"); err == nil || strings.Contains(err.Error(), "unknown") {
		t.Fatalf("baseline skip: %v", err)
	}
}

// A skip token: before the cue, one a week, the day goes neutral, the gate
// needs nothing tonight and no offers come.
func TestSkipTonight(t *testing.T) {
	actionEnv(t)
	s := consState(t, nov(10, 18, 0))
	rep(s, 9)
	s.save()
	msg, err := act("skiptonight")
	if err != nil || !strings.Contains(msg, "chain waits") {
		t.Fatal(msg, err)
	}
	s, _ = load()
	if got, need := s.today(); got != 0 || need != 0 || s.closed() {
		t.Fatalf("token night: %d/%d", got, need)
	}
	if st := edgeStatus(s); st.SkipTokens != 0 || st.CueMs != 0 || st.Chain != 1 {
		t.Fatalf("status: %+v", st)
	}
	if !s.Snooze.After(nov(10, 23, 59)) {
		t.Fatal("offers should rest tonight")
	}
	at(t, nov(11, 21, 0))
	if cs := s.chain(); cs.Chain != 1 || cs.Missed != 0 {
		t.Fatalf("token day is neutral: %+v", cs)
	}
	at(t, nov(11, 18, 0))
	if _, err := act("skiptonight"); err == nil || !strings.Contains(err.Error(), "no skips left") {
		t.Fatalf("second token in a week: %v", err)
	}
	at(t, nov(16, 19, 45)) // Monday: a new token, but past the cue
	if _, err := act("skiptonight"); err == nil || !strings.Contains(err.Error(), "passed") {
		t.Fatalf("after the cue: %v", err)
	}
	at(t, nov(16, 19, 0))
	if _, err := act("skiptonight"); err != nil {
		t.Fatalf("new week: %v", err)
	}
}

// A live round holds the evening: no cue cards.
func TestCueQuietInALiveRound(t *testing.T) {
	s := consState(t, nov(9, 18, 0))
	resetConsRT(t)
	s.Rounds = append(s.Rounds, Round{ID: "cf:2272", Start: nov(9, 17, 50), DurSec: 7200, Plan: "in"})
	consStep(t, s, nov(9, 18, 0), termWin)
	for _, e := range consStep(t, s, nov(9, 18, 1), browserWin) {
		if strings.HasPrefix(e.Key, "cue") {
			t.Fatalf("in a round: %s", e.Key)
		}
	}
}
