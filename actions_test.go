package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// actState saves s with a started task and a settled missed round that owes
// upsolves, so every key in ActionKeys has something to act on.
func actState(t *testing.T) *State {
	t.Helper()
	actionEnv(t)
	at(t, wed)
	s := fresh(t)
	missedRound(s, "cf:2272", wed.Add(-4*time.Hour))
	pending(s)
	s.Current.Policy = "instant"
	begin(s, s.Current, now().Add(-time.Hour))
	s.Current.WorkSec = 3600
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	return s
}

// Every key the adapter can send is one edge handles: none answers
// "unknown action". ActionKeys is the shared list.
func TestEveryActionKeyIsHandled(t *testing.T) {
	for _, k := range ActionKeys {
		t.Run(k, func(t *testing.T) {
			actState(t)
			key := strings.NewReplacer("<id>", "cf:2257C", "<round>", "cf:2272").Replace(k)
			if strings.HasPrefix(key, "undo:") {
				if _, err := act("skipround:cf:2272"); err != nil {
					t.Fatal(err)
				}
				at(t, wed.Add(5*time.Second))
			}
			if _, err := act(key); err != nil && strings.Contains(err.Error(), "unknown action") {
				t.Errorf("%s: %v", key, err)
			}
		})
	}
	actState(t)
	for _, bad := range []string{"open round", "frobnicate", "next:cf:1A", "giveup", "skip"} {
		if _, err := act(bad); err == nil || !strings.Contains(err.Error(), "unknown action") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// A key naming a task that's no longer current is an error, never an
// action on the task that is.
func TestStaleKeyIsAnError(t *testing.T) {
	actState(t)
	for _, k := range []string{"start:cf:1A", "open:cf:1A", "hint:cf:1A", "giveup:cf:1A", "skip:cf:1A", "explain:cf:1A", "debrief:cf:9999"} {
		if _, err := act(k); err == nil {
			t.Errorf("%s acted", k)
		}
	}
	s, _ := load()
	if s.Current.ID != "cf:2257C" || s.Current.Hints != 0 {
		t.Errorf("current changed: %+v", s.Current)
	}
}

func TestSkipAndGiveUpRules(t *testing.T) {
	actState(t) // a new problem, an hour in
	if _, err := act("skip:cf:2257C"); err == nil {
		t.Error("an hour into a new problem, skip should point at give up")
	}
	if _, err := act("giveup:cf:2257C"); err != nil {
		t.Errorf("give up after an hour (instant): %v", err)
	}

	actState(t)
	s, _ := load()
	s.Current.Policy, s.Current.WorkSec = "ladder", 600
	s.save()
	if _, err := act("giveup:cf:2257C"); err == nil || !strings.Contains(err.Error(), "unlocks after") {
		t.Errorf("give up at 10 min on the ladder: %v", err)
	}
	if _, err := act("hint:cf:2257C"); err == nil || !strings.Contains(err.Error(), "unlocks after") {
		t.Errorf("hint before the ladder's first: %v", err)
	}
	s.Current.WorkSec = 300
	s.save()
	at(t, wed.Add(time.Minute))
	if msg, err := act("skip:cf:2257C"); err != nil || !strings.Contains(msg, "Skipped") {
		t.Errorf("skip at 5 min: %q %v", msg, err)
	}
	s, _ = load()
	if h := s.History[len(s.History)-1]; h.ID != "cf:2257C" || h.Outcome != "skipped" {
		t.Errorf("skip recorded: %+v", h)
	}
}

// upsolve:<round> parks an unstarted task (a re-solve goes back to the
// probes) and starts that round's next owed problem.
func TestUpsolveRoundParksUnstarted(t *testing.T) {
	actionEnv(t)
	at(t, wed)
	s := fresh(t)
	missedRound(s, "cf:2261", wed.Add(-48*time.Hour))
	missedRound(s, "cf:2272", wed.Add(-4*time.Hour))
	s.Probes = append(s.Probes, Probe{Problem: "cf:1360E", Title: "Polygon", Due: wed.Add(-time.Hour), Stage: 1})
	s.Current = &Task{ID: "cf:1360E", Kind: "resolve", Title: "Polygon", Assigned: now(), Explain: -1, Policy: "cold", ProbeStage: 1}
	s.Probes = nil
	s.save()
	if _, err := act("upsolve:cf:2272"); err != nil {
		t.Fatal(err)
	}
	s, _ = load()
	if s.Current.Round != "cf:2272" || s.Current.ID != "cf:2272C" || !s.Current.Active() {
		t.Errorf("current %+v", s.Current)
	}
	if len(s.Probes) != 1 || s.Probes[0].Problem != "cf:1360E" {
		t.Errorf("the re-solve should be back in the probes: %+v", s.Probes)
	}
	if _, err := act("upsolve:cf:9"); err == nil {
		t.Error("an unknown round")
	}
}

// next hands out a problem before study time too.
func TestNextBeforeStudyTime(t *testing.T) {
	actionEnv(t)
	at(t, wed.Add(-8*time.Hour)) // 13:00
	s := fresh(t)
	s.Probes = append(s.Probes, Probe{Problem: "cf:1360E", Title: "Polygon", Due: now().Add(-time.Hour), Stage: 1})
	s.save()
	if _, err := act("next"); err != nil {
		t.Fatal(err)
	}
	s, _ = load()
	if s.Current == nil || s.Current.ID != "cf:1360E" || !s.Current.Active() {
		t.Errorf("next at 13:00: %+v", s.Current)
	}
}

func TestSkipRoundUndo(t *testing.T) {
	actionEnv(t)
	at(t, wed)
	s := fresh(t)
	s.Rounds = append(s.Rounds, Round{ID: "cf:2280", Name: "Codeforces Round 9 (Div. 2)", Kind: "div2", Start: wed.Add(time.Hour), DurSec: 7200, Plan: "in"})
	s.save()
	if _, err := act("undo:skipround:cf:2280"); err == nil {
		t.Error("nothing to undo yet")
	}
	if _, err := act("skipround:cf:2280"); err != nil {
		t.Fatal(err)
	}
	if s, _ := load(); s.round("cf:2280").Plan != "out" {
		t.Fatal("skipround")
	}
	if msg, err := act("undo:skipround:cf:2280"); err != nil || !strings.HasPrefix(msg, "Back in") {
		t.Fatalf("undo: %q %v", msg, err)
	}
	if s, _ := load(); s.round("cf:2280").Plan != "in" || s.round("cf:2280").PlanWas != "" {
		t.Errorf("after undo: %+v", s.round("cf:2280"))
	}
}

// The status carries what the island needs to offer the right buttons.
func TestStatusFields(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	if st := edgeStatus(s); st.GiveUpInSec != -1 || st.TaskID != "" || st.SeenMs != wed.UnixMilli() {
		t.Errorf("no task: %+v", st)
	}
	missedRound(s, "cf:2272", wed.Add(-4*time.Hour))
	pending(s)
	st := edgeStatus(s)
	if st.TaskID != "cf:2257C" || st.Short != "2257C" || st.Kind != "new" || !st.CanSkip || st.GiveUpInSec != -1 || st.HintsReady {
		t.Errorf("pending: %+v", st)
	}
	if len(st.Owed) != 1 || st.Owed[0].Round != "cf:2272" || strings.Join(st.Owed[0].Indexes, " ") != "C D" || st.Owed[0].DueMs == 0 {
		t.Errorf("owed: %+v", st.Owed)
	}
	begin(s, s.Current, now())
	s.Current.Policy = "ladder"
	s.Current.addWork(now(), 25*60)
	st = edgeStatus(s)
	if !st.HintsReady || st.NextHintSec == 0 || st.GiveUpInSec <= 0 || st.WorkSec != 1500 || st.TodaySec != 1500 || st.Policy != "ladder" {
		t.Errorf("working: %+v", st)
	}
	s.Current = nil
	s.Current = assignUpsolve(s, s.round("cf:2272"), s.round("cf:2272").owed(now()))
	if st := edgeStatus(s); st.DueMs != s.round("cf:2272").Problems[2].Due.UnixMilli() || st.Lapsed || !st.CanSkip {
		t.Errorf("upsolve: %+v", st)
	}
	s.Trouble["cf"] = Trouble{Since: now().Add(-time.Hour), Msg: `can't reach Codeforces: Get "https://codeforces.com/api/user.status?handle=x": timeout`}
	if st := edgeStatus(s); strings.Contains(st.Health, "http") || !strings.Contains(st.Health, "Codeforces isn't answering") {
		t.Errorf("health: %q", st.Health)
	}
	if short(&Task{ID: "lc:two-sum", Title: "3542. Two Sum"}) != "LC 3542" || short(&Task{ID: "ac:abc482_d"}) != "ABC482 D" {
		t.Error("short labels")
	}
}

// R5: two offer cards an evening at most, then the pill alone.
func TestOfferTwiceAnEvening(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	pending(s)
	n := 0
	for m := 0; m < 180; m += 10 {
		at(t, wed.Add(time.Duration(m)*time.Minute))
		if strings.Contains(keys(tell(s, window{}, 0)), "offer:") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("offers in 3 hours: %d", n)
	}
	at(t, wed.Add(23*time.Hour)) // the next evening
	if !strings.Contains(keys(tell(s, window{}, 0)), "offer:") {
		t.Error("a new evening, a new offer")
	}
}

// The give-up card comes back every 30 min while it holds; the late
// warning runs past midnight and has a Start.
func TestGiveUpRepeatsAndLateAfterMidnight(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	pending(s)
	begin(s, s.Current, now())
	s.Current.Policy, s.Current.WorkSec = "ladder", 3*3600
	s.Current.Hints = 3
	if !strings.Contains(keys(tell(s, window{}, 0)), "giveup:cf:2257C") {
		t.Fatal("give-up card")
	}
	at(t, wed.Add(31*time.Minute))
	if !strings.Contains(keys(tell(s, window{}, 0)), "giveup:cf:2257C") {
		t.Error("the give-up card should come back after 30 min")
	}
	at(t, wed.Add(4*time.Hour+30*time.Minute)) // 01:30, the day runs to 04:00
	var late *event
	for _, e := range plan(s, window{}, 0) {
		if e.slot() == "late" {
			late = &e
		}
	}
	if late == nil || late.Title != "1 task left today" || len(late.Actions) != 1 || late.Actions[0].Key != "start:cf:2257C" {
		t.Errorf("late at 01:30: %+v", late)
	}
}

// fakeEmber answers each edge message with ok (or not), and records them.
func fakeEmber(t *testing.T, ok *bool) (got func() []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	l, err := net.Listen("unix", filepath.Join(dir, "ember.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var mu sync.Mutex
	var msgs []map[string]any
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var m struct {
				Payload map[string]any `json:"payload"`
			}
			json.Unmarshal(line, &m)
			mu.Lock()
			yes := *ok
			if yes {
				msgs = append(msgs, m.Payload)
			}
			mu.Unlock()
			if yes {
				c.Write([]byte(`{"ok":true}` + "\n"))
			} else {
				c.Write([]byte(`{"ok":false}` + "\n"))
			}
			c.Close()
		}
	}()
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), msgs...)
	}
}

// A card Ember refuses (no edge plugin) isn't said; it's held and goes out
// with the next ok:true. A card whose task ended is taken down.
func TestDeliveryHoldsRefusedCards(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	pending(s)
	ok := false
	got := fakeEmber(t, &ok)
	var l emberLink
	st := edgeStatus(s)
	offer := event{Key: "offer:cf:2257C:2026-10-14:1", Title: "Next problem is ready", Actions: []action{{"start:cf:2257C", "Start"}}}
	if said := l.tick(st, []event{offer}, nil); len(said) != 0 {
		t.Fatalf("refused but said: %v", said)
	}
	ok = true
	said := l.tick(st, nil, nil)
	if len(said) != 1 || said[0] != offer.Key {
		t.Fatalf("held card not delivered on ok:true: %v", said)
	}
	cards := 0
	for _, m := range got() {
		if m["type"] == "card" {
			cards++
			if c := m["card"].(map[string]any); c["ttl_s"] == nil {
				t.Errorf("a card with buttons needs ttl_s: %v", c)
			}
		}
	}
	if cards != 1 {
		t.Errorf("cards delivered: %d", cards)
	}
	begin(s, s.Current, now()) // the offer is over
	l.tick(edgeStatus(s), nil, nil)
	removed := false
	for _, m := range got() {
		removed = removed || m["type"] == "remove" && m["id"] == "offer"
	}
	if !removed {
		t.Error("the offer card should be removed once started")
	}
}

func TestEventLogRotates(t *testing.T) {
	at(t, wed)
	fresh(t)
	logEvent("start", map[string]any{"task": "cf:1A"})
	b, err := os.ReadFile(eventLogPath())
	var m map[string]any
	if err != nil || json.Unmarshal(b, &m) != nil || m["ev"] != "start" || m["task"] != "cf:1A" {
		t.Fatalf("event log: %s %v", b, err)
	}
	os.WriteFile(eventLogPath(), make([]byte, eventLogMax), 0o644)
	logEvent("skip", nil)
	if st, _ := os.Stat(eventLogPath()); st.Size() > 200 {
		t.Errorf("not rotated: %d", st.Size())
	}
	if _, err := os.Stat(eventLogPath() + ".1"); err != nil {
		t.Error(err)
	}
}
