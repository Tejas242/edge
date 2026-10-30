package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Trial 1, the consistency loop: one rep a night, asked for at the first
// unlock after office, a chain that forgives one miss, and skip tokens.
// edge records every day quietly in every phase (so the baseline is measured
// the same way); the cards, the status fields and the rep rule for the gate
// switch on only in trial1 and trial2.

const (
	repFocus      = 25 * time.Minute // focused work that makes a day's rep
	repFocusBack  = 20 * time.Minute // the bar the day after a miss: never miss twice
	idleGap       = 90 * time.Minute // asleep, locked or idle this long: the next activity is an unlock
	cueNudgeAfter = 15 * time.Minute // no start this long after the cue: one gentle card
	cueNudgeUntil = 3 * time.Hour    // and not later than this after it
	cueCardFor    = 2 * time.Hour    // an unlock's card not delivered by then is dropped
	repClock      = 45 * time.Minute // the interview countdown on a started rep
	seenEvery     = 10 * time.Minute // SeenAt is written at most this often
	consKeep      = 60               // days of records kept
)

// Consistency is the Trial 1 state: tonight's cue, reps, the chain and skip
// tokens.
type Consistency struct {
	Days     []ConsDay     `json:"days,omitempty"`     // one record per edge day the daemon saw, oldest first
	Tokens   []string      `json:"tokens,omitempty"`   // days a skip token made neutral ("2026-11-09")
	SeenAt   time.Time     `json:"seen_at,omitzero"`   // the last activity seen, to within seenEvery
	Chain    int           `json:"chain,omitempty"`    // the chain at the last step, to see it break
	Verdict  *TrialVerdict `json:"verdict,omitempty"`  // Trial 1's result, once its window is over
	Reverted bool          `json:"reverted,omitempty"` // Trial 1 ended under its line: its rules are off
}

// ConsDay is one day's record: the rep, the cue and how long the start took.
type ConsDay struct {
	Date     string    `json:"date"`  // the edge day, "2026-11-06"
	Phase    string    `json:"phase"` // the phase it began in
	Rep      bool      `json:"rep,omitempty"`
	Kind     string    `json:"kind,omitempty"` // ac | round | giveup | focus | submit
	RepAt    time.Time `json:"rep_at,omitzero"`
	CueSet   bool      `json:"cue_set,omitempty"`   // he picked the time on the card
	CueAt    time.Time `json:"cue_at,omitzero"`     // the time he picked
	UnlockAt time.Time `json:"unlock_at,omitzero"`  // the first unlock after office
	CardAt   time.Time `json:"card_at,omitzero"`    // the "When?" card reached him
	StartAt  time.Time `json:"start_at,omitzero"`   // work began
	Nudged   bool      `json:"nudged,omitempty"`    // the 15-minute card went out
	TokenAt  time.Time `json:"token_at,omitzero"`   // a skip token made the day neutral
	RepFocus int       `json:"rep_focus,omitempty"` // minutes of focus the day needed (20 after a miss)
}

// TrialVerdict is Trial 1's pre-registered result.
type TrialVerdict struct {
	Reps     int       `json:"reps"`
	Days     int       `json:"days"`
	Reverted bool      `json:"reverted"`
	At       time.Time `json:"at"`
}

func dateKey(d time.Time) string { return d.Format(time.DateOnly) }

// rec is the record for date, or nil.
func (c *Consistency) rec(date string) *ConsDay {
	for i := len(c.Days) - 1; i >= 0; i-- {
		if c.Days[i].Date == date {
			return &c.Days[i]
		}
	}
	return nil
}

// day is today's record, made on first sight; old records are dropped.
func (c *Consistency) day(date, phase string) *ConsDay {
	if r := c.rec(date); r != nil {
		return r
	}
	c.Days = append(c.Days, ConsDay{Date: date, Phase: phase})
	if len(c.Days) > consKeep {
		c.Days = slices.Clone(c.Days[len(c.Days)-consKeep:])
	}
	return &c.Days[len(c.Days)-1]
}

func (c *Consistency) token(date string) bool { return slices.Contains(c.Tokens, date) }

// tokensLeft are this week's skip tokens not yet used (Mon-Sun).
func (s *State) tokensLeft(n time.Time) int {
	from := dateKey(weekStart(n))
	used := 0
	for _, d := range s.Cons.Tokens {
		if d >= from {
			used++
		}
	}
	return max(0, cfg().SkipTokens-used)
}

// repRank orders the reasons a day counts; the strongest names it.
var repRank = map[string]int{"submit": 1, "focus": 2, "giveup": 3, "round": 4, "ac": 5}

// repDays says, per edge day, why it has a rep ("" = none), from the state
// alone so the gate's process sees what the daemon sees: an accepted
// solution, a round sat, a give-up after 25 focused minutes, 25 focused
// minutes in all, or a submission. Days the daemon latched at the lower bar
// after a miss keep their rep. work is the focused seconds per day.
func (s *State) repDays() (kinds map[string]string, work map[string]int) {
	kinds, work = map[string]string{}, map[string]int{}
	set := func(date, k string) {
		if repRank[k] > repRank[kinds[date]] {
			kinds[date] = k
		}
	}
	sub := func(t *Task) {
		if t.SubSeen > 0 {
			set(dateKey(day(time.Unix(t.SubSeen, 0))), "submit")
		}
	}
	for i := range s.History {
		t := &s.History[i]
		sub(t)
		switch t.Outcome {
		case "solo", "assisted":
			set(dateKey(day(t.Finished)), "ac")
		case "gaveup":
			if t.Worked() >= repFocus {
				set(dateKey(day(t.Finished)), "giveup")
			}
		}
	}
	if t := s.Current; t != nil {
		sub(t)
	}
	for i := range s.Rounds {
		if r := &s.Rounds[i]; r.Joined != "" {
			set(dateKey(day(r.creditAt())), "round")
		}
	}
	s.eachWork(func(d time.Time, sec int) { work[dateKey(d)] += sec })
	for date, sec := range work {
		if time.Duration(sec)*time.Second >= repFocus {
			set(date, "focus")
		}
	}
	for _, r := range s.Cons.Days {
		if r.Rep {
			set(r.Date, r.Kind)
		}
	}
	return kinds, work
}

// chainState is the chain as of now.
type chainState struct {
	Chain   int    // days with a rep in a row, one miss forgiven; today's rep included
	Missed  int    // misses in a row right before today: 0 or 1 (two break the chain)
	RepDone bool   // today has its rep
	Kind    string // why
	Token   bool   // a skip token made today neutral
	Bar     time.Duration
}

// chain walks back from yesterday: a rep day adds one, a token day is
// neutral (it neither counts nor ends a run of misses), one miss is forgiven,
// two in a row end it. Today adds one once its rep is done; until then it's
// pending, never a miss. After a break the chain is 0 (shown as nothing) and
// the next rep makes it 1.
func (s *State) chain() chainState {
	n := now()
	kinds, work := s.repDays()
	today := day(n)
	var cs chainState
	misses, lead := 0, true
	for d, i := today.AddDate(0, 0, -1), 0; i < 3650; d, i = d.AddDate(0, 0, -1), i+1 {
		k := dateKey(d)
		switch {
		case kinds[k] != "":
			cs.Chain++
			misses, lead = 0, false
		case s.Cons.token(k):
		default:
			misses++
			if lead {
				cs.Missed = misses
			}
		}
		if misses >= 2 {
			break
		}
	}
	if cs.Missed >= 2 {
		cs.Missed = 0 // broken: a fresh start, not a second miss
	}
	tk := dateKey(today)
	cs.Bar = repFocus
	if cs.Missed == 1 {
		cs.Bar = repFocusBack
	}
	cs.Kind = kinds[tk]
	if cs.Kind == "" && time.Duration(work[tk])*time.Second >= cs.Bar {
		cs.Kind = "focus"
	}
	cs.RepDone = cs.Kind != ""
	if cs.RepDone {
		cs.Chain++
	}
	cs.Token = !cs.RepDone && s.Cons.token(tk)
	return cs
}

// consToday is today() in the trial phases: one rep, every day, weekends
// included. A token night needs nothing.
func (s *State) consToday() (got, need int) {
	switch cs := s.chain(); {
	case cs.RepDone:
		return 1, 1
	case cs.Token:
		return 0, 0
	}
	return 0, 1
}

// ConsistencyStats is its part of `edge stats`.
type ConsistencyStats struct{}

func (s *State) consistencyStats() *ConsistencyStats { return nil }

// consistencyStatus fills the status fields Trial 1 owns, in its phases only.
func consistencyStatus(s *State, st *emberStatus) {
	n := now()
	if !s.consOn(n) {
		return
	}
	cs := s.chain()
	st.RepDone, st.Chain, st.Missed, st.SkipTokens = cs.RepDone, cs.Chain, cs.Missed, s.tokensLeft(n)
}

// consistencyAction handles Trial 1's action keys (cue:…, skiptonight …);
// handled=false leaves the key to others.
func consistencyAction(s *State, verb, arg string) (handled bool, msg string, err error) {
	return false, "", nil
}

// consistencyEvents keeps the day's record in every phase and, in the trial
// phases, says Trial 1's cards.
func consistencyEvents(s *State, w window, n time.Time) []event {
	c := &s.Cons
	ph := s.phase(n)
	r := c.day(dateKey(day(n)), ph)
	cs := s.chain()
	s.latch(r, cs, n)
	if c.Chain > 0 && cs.Chain == 0 {
		logEvent("chain_break", map[string]any{"was": c.Chain, "phase": ph})
	}
	c.Chain = cs.Chain
	return nil
}

// latch writes what today already proved into its record: the rep (once,
// with its kind and the bar it met) and when work began.
func (s *State) latch(r *ConsDay, cs chainState, n time.Time) {
	if r.StartAt.IsZero() {
		if at := s.startedOn(day(n), n); !at.IsZero() {
			r.StartAt = at
		}
	}
	if cs.RepDone && !r.Rep {
		r.Rep, r.Kind, r.RepAt, r.RepFocus = true, cs.Kind, n, int(cs.Bar.Minutes())
		kv := map[string]any{"date": r.Date, "kind": cs.Kind, "phase": r.Phase, "chain": cs.Chain}
		if m, ok := s.cueToStart(r); ok {
			kv["cue_to_start_min"] = m
		}
		logEvent("rep", kv)
	}
}

// startedOn is when work began on edge day d, as of n: a task started that
// day, a round sat, or, for a task carried over, now minus the day's work.
func (s *State) startedOn(d, n time.Time) time.Time {
	var first time.Time
	take := func(at time.Time) {
		if !at.IsZero() && day(at).Equal(d) && (first.IsZero() || at.Before(first)) {
			first = at
		}
	}
	for i := range s.History {
		take(s.History[i].Started)
	}
	for i := range s.Rounds {
		if r := &s.Rounds[i]; r.Joined != "" {
			take(r.creditAt())
		}
	}
	if t := s.Current; t != nil {
		take(t.Started)
		if sec := t.WorkDays[dateKey(d)]; sec > 0 && first.IsZero() {
			take(n.Add(-time.Duration(sec) * time.Second))
		}
	}
	return first
}

// cueToStart is the minutes from the day's cue to its start (negative: he
// began before the cue).
func (s *State) cueToStart(r *ConsDay) (int, bool) {
	if r == nil || r.StartAt.IsZero() {
		return 0, false
	}
	d, err := time.ParseInLocation(time.DateOnly, r.Date, time.Local)
	if err != nil {
		return 0, false
	}
	return int(r.StartAt.Sub(cueAt(r, d)).Round(time.Minute).Minutes()), true
}

// defaultCue is the rep time on edge day d unless he picks one.
func defaultCue(d time.Time) time.Time {
	if weekend(d.Add(12 * time.Hour)) {
		return d.Add(cfg().CueWeekend)
	}
	return d.Add(cfg().CueWeekday)
}

// cueAt is the day's cue: the time he picked; else, when the card came after
// the default had passed, the card's time ("Now" was the default); else the
// default.
func cueAt(r *ConsDay, d time.Time) time.Time {
	if r != nil && r.CueSet {
		return r.CueAt
	}
	base := defaultCue(d)
	if r != nil && r.CardAt.After(base) {
		return r.CardAt
	}
	return base
}

// hhmm parses "19:30" into a time on edge day d; times before day_ends are
// that night's small hours.
func hhmm(d time.Time, s string) (time.Time, bool) {
	h, m, ok := strings.Cut(s, ":")
	a, e1 := strconv.Atoi(h)
	b, e2 := strconv.Atoi(m)
	if !ok || e1 != nil || e2 != nil || a < 0 || a > 23 || b < 0 || b > 59 {
		return time.Time{}, false
	}
	off := time.Duration(a)*time.Hour + time.Duration(b)*time.Minute
	if off < cfg().DayEnds {
		off += 24 * time.Hour
	}
	return d.Add(off), true
}

var _ = fmt.Sprint
