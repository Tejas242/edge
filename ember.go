package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Ember (the Noctalia bar plugin, screenager/ember) shows edge's status and
// cards; its buttons call `edge action <key>`.
const emberCore = "screenager/ember:core"

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

// toEmber delivers one message; false means Ember isn't running.
func toEmber(msg any) bool {
	b, _ := json.Marshal(msg)
	cmd := exec.Command("noctalia", "msg", "plugin", emberCore, "all", "edge", string(b))
	done := make(chan error, 1)
	if cmd.Start() != nil {
		return false
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		return false
	}
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

// emberHook is the plugin's hook script, if Ember is installed locally.
func emberHook() string {
	h, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(h, ".local", "share", "noctalia", "plugins", "ember", "hook"),
		filepath.Join(h, ".local", "state", "noctalia", "plugins", "materialized", "community", "ember", "hook"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
