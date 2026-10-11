package island

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	surfW, surfH = 420, 240
	marginTop    = 4
	flickerEvery = time.Second / 12
)

type app struct {
	ui    *UI
	win   *window
	brain chan string // actions back to edge's brain
}

// Run opens the island and serves until the compositor goes away.
func Run() error {
	posts := make(chan func(*app), 64)
	if _, err := listen(posts); err != nil {
		return err
	}
	win, err := openWindow(surfW, surfH, marginTop)
	if err != nil {
		return err
	}
	a := &app{ui: NewUI(surfW, surfH), win: win}
	a.ui.onKey = a.key
	frameDue := false
	win.onPointer = func(e pointerEvent) {
		redraw, hand := a.ui.pointer(e)
		win.setCursor(hand)
		if redraw {
			a.ui.Invalidate()
		}
	}
	win.onFrame = func() { frameDue = true }
	log.Printf("island up (%dx%d)", surfW, surfH)

	animating, waitingFrame, tick := false, false, false
	draw := func() {
		var damage image.Rectangle
		animating, damage = a.ui.renderDamage()
		if win.present(a.ui.cv.img, a.ui.shape, damage, animating) {
			waitingFrame = animating
		}
	}
	draw()
	housekeep := time.NewTicker(20 * time.Second)
	defer housekeep.Stop()
	timer := time.NewTimer(time.Hour)
	for {
		select {
		case m := <-win.c.msgs:
			win.c.dispatch(m)
		case err := <-win.c.readErr:
			return err
		case f := <-posts:
			f(a)
			a.ui.Invalidate()
		case <-housekeep.C:
			if a.sweep() {
				a.ui.Invalidate()
			}
		case <-timer.C:
			tick = true
			if a.ui.expire() {
				a.ui.Invalidate()
			}
		}
		if win.closed {
			return fmt.Errorf("layer surface closed")
		}
		switch {
		case frameDue:
			frameDue, waitingFrame = false, false
			if animating || a.ui.dirty {
				draw()
			}
		case (a.ui.dirty || tick) && !waitingFrame:
			draw()
		}
		tick = false
		timer.Reset(a.nextWake(animating))
		win.c.flush()
	}
}

// nextWake: animation runs on frame callbacks; otherwise the loop sleeps
// until the next blink, flicker tick, minute change or expiry.
func (a *app) nextWake(animating bool) time.Duration {
	now := a.ui.now()
	if animating {
		return time.Second // frame callbacks drive it
	}
	next := a.ui.nextBlink()
	if a.ui.continuous() {
		next = now.Add(flickerEvery)
	}
	if m := now.Truncate(time.Minute).Add(time.Minute); m.Before(next) {
		next = m
	}
	for _, c := range a.ui.cards {
		if !c.Expires.IsZero() && c.Expires.Before(next) {
			next = c.Expires
		}
	}
	if !a.ui.hover && now.Sub(a.ui.leftAt) < 400*time.Millisecond {
		next = a.ui.leftAt.Add(360 * time.Millisecond)
	}
	return max(next.Sub(now), 5*time.Millisecond)
}

// sweep forgets sessions whose Claude process is gone.
func (a *app) sweep() bool {
	changed := false
	for id, s := range a.ui.sessions {
		if !slices.ContainsFunc(s.Pids, alive) || (s.State == "idle" && a.ui.now().Sub(s.Seen) > 3*time.Hour) {
			delete(a.ui.sessions, id)
			changed = true
		}
	}
	return changed
}

func (a *app) key(k string) {
	switch {
	case strings.HasPrefix(k, "focus:"):
		if s := a.ui.sessions[strings.TrimPrefix(k, "focus:")]; s != nil {
			go focusTerminal(s.Pids)
		}
	default:
		a.toBrain(k)
	}
}

func (a *app) toBrain(k string) {
	if a.brain != nil {
		select {
		case a.brain <- k:
		default:
		}
	}
}

// ---- Claude Code hooks ----

// hook updates sessions and, for waiting hooks, puts up a card whose answer
// goes to answer. Returns the card id.
func (a *app) hook(m inMsg, answer chan<- json.RawMessage) string {
	var in hookInput
	json.Unmarshal(m.Payload, &in)
	now := a.ui.now()
	s := a.ui.sessions[in.SessionID]
	if s == nil && in.SessionID != "" && m.Event != "SessionEnd" {
		s = &Session{ID: in.SessionID, State: "idle", Since: now}
		a.ui.sessions[in.SessionID] = s
	}
	if s != nil {
		s.Seen = now
		if in.Cwd != "" {
			s.Project = project(in.Cwd)
		}
		if len(m.Pids) > 0 {
			s.Pids = m.Pids
		}
	}
	if s == nil {
		if answer != nil {
			answer <- nil
		}
		return ""
	}
	var tin map[string]any
	json.Unmarshal(in.ToolInput, &tin)

	switch m.Event {
	case "SessionEnd":
		delete(a.ui.sessions, in.SessionID)
	case "UserPromptSubmit":
		s.State, s.Label, s.Plus, s.Minus, s.Since = "work", "Thinking", 0, 0, now
	case "PreToolUse":
		if in.Tool == "AskUserQuestion" && answer != nil {
			s.State = "wait"
			return a.ask(s, in, tin, answer)
		}
		s.State = "work"
		s.Label, s.Plus, s.Minus = activity(in.Tool, tin)
	case "PostToolUse":
		if s.State == "wait" {
			s.State = "work"
		}
	case "PermissionRequest":
		if answer == nil {
			return ""
		}
		s.State = "wait"
		return a.permission(s, in, tin, answer)
	case "Notification":
		if in.NotifyType == "idle_prompt" {
			s.State = "wait"
		}
	case "Stop":
		s.State, s.Label = "idle", ""
		if slices.Contains(s.Pids, focusedPid()) {
			break // they're looking at it
		}
		body := strings.TrimSpace(in.LastMessage)
		if i := strings.IndexByte(body, '\n'); i > 0 {
			body = body[:i]
		}
		id := "done:" + s.ID
		a.ui.AddCard(&Card{ID: id, Kind: "done", Mood: moodHappy, Title: "Done", Meta: s.Project,
			Body: strings.TrimLeft(body, "#*- "), Expires: now.Add(9 * time.Second), Priority: 1,
			Buttons: []Button{{Key: "open", Label: "Open terminal"}},
			OnPick: func(k string, _ []int) {
				if k == "open" {
					go focusTerminal(s.Pids)
				}
			}})
		a.ui.Bounce()
	}
	return ""
}

func (a *app) permission(s *Session, in hookInput, tin map[string]any, answer chan<- json.RawMessage) string {
	str := func(k string) string { v, _ := tin[k].(string); return v }
	c := &Card{ID: "perm:" + s.ID + ":" + fmt.Sprint(time.Now().UnixNano()), Kind: "perm", Mood: moodWait,
		Meta: s.Project, Priority: 10, Footer: "In terminal", Expires: a.ui.now().Add(AskTimeout - 5*time.Second)}
	switch in.Tool {
	case "Bash":
		c.Title, c.Body, c.Code = "Run this command?", str("description"), str("command")
	case "Edit", "MultiEdit", "Write":
		verb := map[string]string{"Write": "Create"}[in.Tool]
		if verb == "" {
			verb = "Edit"
		}
		_, p, m := activity(in.Tool, tin)
		c.Title = verb + " " + filepath.Base(str("file_path")) + "?"
		c.Body = fmt.Sprintf("%s  ·  %s", rel(str("file_path"), s), diffText(max(p, 0), m))
		if p == 0 && m == 0 {
			c.Body = rel(str("file_path"), s)
		}
	case "WebFetch":
		u, _ := url.Parse(str("url"))
		c.Title, c.Code = "Fetch "+u.Host+"?", str("url")
	default:
		label, _, _ := activity(in.Tool, tin)
		c.Title = label + "?"
		if b, err := json.Marshal(tin); err == nil && len(b) > 2 {
			c.Code = string(b)
		}
	}
	c.Buttons = []Button{{Key: "deny", Label: "Deny", Danger: true}}
	if len(in.Suggestions) > 2 && string(in.Suggestions) != "null" {
		c.Buttons = append(c.Buttons, Button{Key: "always", Label: "Always"})
	}
	c.Buttons = append(c.Buttons, Button{Key: "allow", Label: "Allow", Primary: true})
	decide := func(decision map[string]any) {
		b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest", "decision": decision}})
		answer <- b
	}
	c.OnPick = func(k string, _ []int) {
		s.State = "work"
		switch k {
		case "allow":
			decide(map[string]any{"behavior": "allow"})
		case "always":
			decide(map[string]any{"behavior": "allow", "updatedPermissions": in.Suggestions})
		case "deny":
			decide(map[string]any{"behavior": "deny", "message": "Denied from the edge island."})
		default: // In terminal: no decision, Claude asks there
			answer <- nil
		}
	}
	c.OnExpire = func() { answer <- nil }
	a.ui.AddCard(c)
	return c.ID
}

func rel(p string, s *Session) string {
	if p == "" {
		return ""
	}
	return strings.TrimPrefix(p, filepath.Dir(p)+"/")
}

type question struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

// ask walks the AskUserQuestion questions one card at a time.
func (a *app) ask(s *Session, in hookInput, tin map[string]any, answer chan<- json.RawMessage) string {
	var qs []question
	b, _ := json.Marshal(tin["questions"])
	json.Unmarshal(b, &qs)
	if len(qs) == 0 {
		answer <- nil
		return ""
	}
	answers := map[string]string{}
	deadline := a.ui.now().Add(AskTimeout - 5*time.Second)
	id := "ask:" + s.ID
	var show func(i int)
	show = func(i int) {
		q := qs[i]
		c := &Card{ID: id, Kind: "ask", Mood: moodWait, Title: q.Question, Meta: s.Project, Priority: 9,
			Multi: q.MultiSelect, Footer: "In terminal", Expires: deadline}
		if len(qs) > 1 {
			c.Meta = fmt.Sprintf("%s · %d/%d", s.Project, i+1, len(qs))
		}
		for _, o := range q.Options {
			if len(c.Options) == 4 {
				break
			}
			c.Options = append(c.Options, Option{o.Label, o.Description})
		}
		if q.MultiSelect {
			c.Buttons = []Button{{Key: "submit", Label: "Submit", Primary: true}}
		}
		c.OnPick = func(k string, picked []int) {
			if k == "footer" || k == "dismiss" || (k == "submit" && len(picked) == 0) {
				answer <- nil
				return
			}
			var labels []string
			for _, p := range picked {
				labels = append(labels, q.Options[p].Label)
			}
			answers[q.Question] = strings.Join(labels, ", ")
			if i+1 < len(qs) {
				show(i + 1)
				return
			}
			s.State = "work"
			b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PreToolUse", "permissionDecision": "allow",
				"updatedInput": map[string]any{"questions": tin["questions"], "answers": answers}}})
			answer <- b
		}
		c.OnExpire = func() { answer <- nil }
		a.ui.AddCard(c)
	}
	show(0)
	return id
}

// ---- edge's brain ----

func (a *app) fromBrain(m inMsg) {
	switch m.Type {
	case "status":
		if m.Status != nil {
			a.ui.edge = *m.Status
		}
	case "remove":
		a.ui.RemoveCard("edge:" + m.ID)
	case "card":
		w := m.Card
		if w == nil {
			return
		}
		moods := map[string]mood{"work": moodWork, "wait": moodWait, "happy": moodHappy, "error": moodError, "nudge": moodNudge}
		c := &Card{ID: "edge:" + w.ID, Kind: "edge", Mood: moods[w.Mood], Title: w.Title, Meta: w.Meta, Body: w.Body,
			Buttons: w.Buttons, Priority: 2}
		if w.Urgent {
			c.Priority = 5
		}
		if len(w.Buttons) == 0 {
			c.Expires = a.ui.now().Add(7 * time.Second)
		}
		key := w.ID
		c.OnPick = func(k string, _ []int) {
			if k != "dismiss" {
				a.toBrain(k)
			} else {
				a.toBrain("dismiss:" + key)
			}
		}
		a.ui.AddCard(c)
		if w.Bounce {
			a.ui.Bounce()
		}
	}
}
