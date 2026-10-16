package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Ember (github.com/Tejas242/ember, the dynamic island) shows edge's status
// and cards; its buttons call `edge action <key>`.

type emberButton struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Primary bool   `json:"primary,omitempty"`
}

type emberCard struct {
	ID      string        `json:"id"`
	Mood    string        `json:"mood"`
	Title   string        `json:"title"`
	Body    string        `json:"body,omitempty"`
	Buttons []emberButton `json:"buttons,omitempty"`
	Urgent  bool          `json:"urgent,omitempty"`
	Bounce  bool          `json:"bounce,omitempty"`
}

type emberStatus struct {
	Today     int         `json:"today"`
	Need      int         `json:"need"`
	Streak    int         `json:"streak"`
	Closed    bool        `json:"closed"`
	Task      string      `json:"task,omitempty"`
	URL       string      `json:"url,omitempty"`
	StartedMs int64       `json:"startedMs,omitempty"`
	Pending   bool        `json:"pending,omitempty"`
	Round     *emberRound `json:"round,omitempty"` // a round that's close, live, or just over
}

// emberRound is the round the island shows: from two hours before it starts
// until half an hour after it settles.
type emberRound struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Short    string         `json:"short"`
	URL      string         `json:"url"`
	StartMs  int64          `json:"startMs"`
	EndMs    int64          `json:"endMs"`
	Plan     string         `json:"plan"`             // in | out | "" (open)
	Joined   string         `json:"joined,omitempty"` // live | virtual
	Locked   bool           `json:"locked,omitempty"` // Claude is off for it
	Settled  bool           `json:"settled,omitempty"`
	Problems []emberProblem `json:"problems,omitempty"`
}

type emberProblem struct {
	Index string `json:"index"`
	State string `json:"state,omitempty"` // "" | tried | solved | upsolve | upsolved | taught | dropped | lapsed
}

// islandRound picks the round worth the island's attention now: a live one
// they're in, else the next one they're held to within two hours, else one
// they sat that settled in the last half hour.
func (s *State) islandRound() *Round {
	n := now()
	if r := s.liveHeld(); r != nil {
		return r
	}
	if r := s.nextHeld(); r != nil && r.Joined == "" && r.Start.Sub(n) <= 2*time.Hour {
		return r
	}
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if r := &s.Rounds[i]; r.Joined != "" && !r.Settled.IsZero() && n.Sub(r.Settled) < 30*time.Minute {
			return r
		}
	}
	return nil
}

// toEmber delivers one message over Ember's socket; false means Ember isn't running.
func toEmber(msg any) bool {
	payload, _ := json.Marshal(msg)
	line, _ := json.Marshal(map[string]any{"type": "edge", "payload": json.RawMessage(payload)})
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	c, err := net.DialTimeout("unix", filepath.Join(dir, "ember.sock"), 300*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	_, err = c.Write(append(line, '\n'))
	return err == nil
}

func edgeStatus(s *State) emberStatus {
	got, need := s.today()
	st := emberStatus{Today: got, Need: need, Streak: s.streak(), Closed: s.closed()}
	if r := s.islandRound(); r != nil && cfg().Rounds {
		er := &emberRound{ID: r.ID, Name: r.Name, Short: r.Short(), URL: r.RoundURL(), StartMs: r.Start.UnixMilli(), EndMs: r.End().UnixMilli(),
			Plan: r.Plan, Joined: r.Joined, Locked: s.roundLock() == r, Settled: !r.Settled.IsZero()}
		for _, p := range r.Problems {
			er.Problems = append(er.Problems, emberProblem{Index: p.Index, State: p.State})
		}
		st.Round = er
	}
	if t := s.Current; t != nil {
		switch t.Source() {
		case "lc":
			st.Task = t.Title
		case "ac":
			st.Task = strings.ToUpper(acContest(t.ID)) + " " + indexOf(t.ID) + " · " + t.Title
		default:
			st.Task = strings.TrimPrefix(t.ID, "cf:") + " · " + t.Title
		}
		st.URL, st.Pending = t.URL, !t.Active()
		if t.Active() {
			st.StartedMs = t.Started.UnixMilli()
		}
	}
	return st
}

// emberCardFor turns a brain event into an Ember card, primary action last.
func emberCardFor(e event) emberCard {
	mood := map[string]string{"offer": "work", "hint": "nudge", "cold": "nudge", "giveup": "nudge", "drift": "nudge",
		"late": "error", "ac": "happy", "fail": "error", "brief": "idle",
		"roundeve": "work", "roundsoon": "work", "roundlate": "nudge", "rounddrift": "nudge", "roundover": "happy",
		"roundmissed": "nudge", "rated": "happy"}[e.slot()]
	c := emberCard{ID: e.slot(), Mood: mood, Title: e.Title, Body: e.Body, Urgent: e.Urgent, Bounce: e.slot() == "ac"}
	primary := map[string]bool{"start": true, "next": true, "hint": true, "teach": true, "explain": true, "upsolve": true,
		"register": true, "openround": true}
	var lead []emberButton
	for _, a := range e.Actions {
		verb, _, _ := strings.Cut(a.Key, ":")
		b := emberButton{Key: a.Key, Label: a.Label, Primary: primary[verb]}
		if b.Primary {
			lead = append(lead, b)
		} else {
			c.Buttons = append(c.Buttons, b)
		}
	}
	c.Buttons = append(c.Buttons, lead...)
	return c
}
