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
	Today     int    `json:"today"`
	Need      int    `json:"need"`
	Streak    int    `json:"streak"`
	Closed    bool   `json:"closed"`
	Task      string `json:"task,omitempty"`
	URL       string `json:"url,omitempty"`
	StartedMs int64  `json:"startedMs,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
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
	if t := s.Current; t != nil {
		st.Task = strings.TrimPrefix(t.ID, "cf:") + " · " + t.Title
		if t.Source() == "lc" {
			st.Task = t.Title
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
		"late": "error", "ac": "happy", "fail": "error", "brief": "idle"}[e.slot()]
	c := emberCard{ID: e.slot(), Mood: mood, Title: e.Title, Body: e.Body, Urgent: e.Urgent, Bounce: e.slot() == "ac"}
	primary := map[string]bool{"start": true, "next": true, "hint": true, "teach": true, "explain": true}
	var lead []emberButton
	for _, a := range e.Actions {
		b := emberButton{Key: a.Key, Label: a.Label, Primary: primary[a.Key]}
		if b.Primary {
			lead = append(lead, b)
		} else {
			c.Buttons = append(c.Buttons, b)
		}
	}
	c.Buttons = append(c.Buttons, lead...)
	return c
}
