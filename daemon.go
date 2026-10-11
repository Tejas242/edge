package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	tickEvery = 20 * time.Second
	pollEvery = 60 * time.Second
	reOffer   = 30 * time.Minute
)

type action struct{ Key, Label string }

type event struct {
	Key     string        // dedupe key: said once, or again after Every
	Every   time.Duration // 0 = once
	Title   string
	Body    string
	Urgent  bool
	Actions []action
}

// slot groups events so a newer one replaces the older on screen.
func (e event) slot() string { return strings.SplitN(e.Key, ":", 2)[0] }

type window struct{ App, Title string }

var distraction = regexp.MustCompile(`(?i)youtube|reddit|twitter|\bx\.com\b|instagram|netflix|twitch|hotstar|prime video|facebook|9gag`)

// studyTime: weekday evenings after work, weekends from late morning.
func studyTime(t time.Time) bool {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return t.Hour() >= 10
	}
	return t.Hour() >= 19
}

func (s *State) wantsWork() bool {
	got, need := s.today()
	return got < need || s.creditsSince(weekStart(now())) < weeklyTarget
}

// step is one daemon tick: sync, assign, poll, judge. Returns what to say.
func step(s *State, w window) []event {
	n := now()
	if n.Sub(s.LastSync) > 6*time.Hour {
		if err := sync(s); err != nil {
			log.Println("sync:", err)
		}
	}
	if s.Current == nil && studyTime(n) && !n.Before(s.Snooze) && s.wantsWork() {
		if t, err := assign(s); err != nil {
			log.Println("assign:", err)
		} else {
			log.Println("assigned", t.ID)
		}
	}
	var evs []event
	if t := s.Current; t != nil {
		if !t.Active() {
			if st, err := os.Stat(solutionPath(t)); err == nil && st.ModTime().After(t.Assigned) {
				begin(t, st.ModTime()) // Competitive Companion / nvim created the file: they've started
			}
		}
		if n.Sub(s.LastPoll) >= pollEvery {
			s.LastPoll = n
			evs = append(evs, poll(s, t)...)
		}
	}
	evs = append(evs, plan(s, w)...)
	return s.filter(evs)
}

// filter drops what was already said and records the rest.
func (s *State) filter(evs []event) []event {
	n := now()
	for k, at := range s.Said {
		if n.Sub(at) > 14*24*time.Hour {
			delete(s.Said, k)
		}
	}
	var out []event
	for _, e := range evs {
		if at, ok := s.Said[e.Key]; ok && (e.Every == 0 || n.Sub(at) < e.Every) {
			continue
		}
		s.Said[e.Key] = n
		out = append(out, e)
	}
	return out
}

// plan is the judgement: what's worth saying right now. No IO.
func plan(s *State, w window) []event {
	n := now()
	date := n.Format("2006-01-02")
	got, need := s.today()
	var evs []event

	if n.Hour() >= 7 {
		evs = append(evs, event{Key: "brief:" + date, Title: "edge · today", Body: briefing(s)})
	}
	if n.Hour() >= 22 && got < need && !n.Before(s.GateFrom) && !s.passOn(n) {
		evs = append(evs, event{Key: "late:" + date, Urgent: true, Title: fmt.Sprintf("%d task(s) left today", need-got),
			Body: "Claude stays locked outside ~/CP until it's done."})
	}

	t := s.Current
	if t == nil {
		return evs
	}
	if !t.Active() {
		if studyTime(n) && !n.Before(s.Snooze) {
			acts := []action{{"start", "Start"}, {"snooze", "In 30 min"}}
			if got >= need {
				acts = append(acts, action{"rest", "Done for today"})
			}
			evs = append(evs, event{Key: "offer:" + t.ID, Every: reOffer, Title: offerTitle(t),
				Body: fmt.Sprintf("%s · rating %d\nToday %d/%d · streak %d", t.Title, t.Rating, got, need, s.streak()), Actions: acts})
		}
		return evs
	}

	el := n.Sub(t.Started)
	in := since(t.Started)
	switch t.Policy {
	case "ladder":
		for l := 3; l >= 1; l-- {
			if el >= ladderAt[l-1] && t.Hints < l {
				evs = append(evs, event{Key: fmt.Sprintf("hint:%s:%d", t.ID, l), Title: fmt.Sprintf("%s in · hint %d unlocked", in, l),
					Body: t.Title, Actions: []action{{"hint", "Get hint"}, {"keep", "Keep going"}}})
				break
			}
		}
	case "instant":
		if t.Hints == 0 && (el >= 15*time.Minute || t.Fails >= 2) {
			evs = append(evs, event{Key: "hint:" + t.ID + ":i1", Title: fmt.Sprintf("%s in · want a nudge?", in),
				Body: t.Title, Actions: []action{{"hint", "Get hint"}, {"keep", "Keep going"}}})
		}
	case "cold":
		if el >= 30*time.Minute {
			evs = append(evs, event{Key: "cold:" + t.ID, Title: fmt.Sprintf("%s into the re-solve", in),
				Body:    "No hints on re-solves. Giving up brings it back in 3 days with the solution taught now.",
				Actions: []action{{"teach", "Give up & learn it"}, {"keep", "Keep going"}}})
		}
	}
	if ok, _ := canGiveUp(t); ok && el >= 60*time.Minute && t.Policy != "cold" {
		evs = append(evs, event{Key: "giveup:" + t.ID, Title: fmt.Sprintf("%s on %s", in, t.Title),
			Body:    "An hour of honest work counts for today either way.",
			Actions: []action{{"teach", "Teach me the solution"}, {"keep", "Keep going"}}})
	}

	if distraction.MatchString(w.Title) {
		if s.DriftAt.IsZero() {
			s.DriftAt = n
		} else if n.Sub(s.DriftAt) >= 2*time.Minute {
			site := distraction.FindString(w.Title)
			evs = append(evs, event{Key: "drift:" + t.ID, Every: 10 * time.Minute, Title: fmt.Sprintf("You're on %s", site),
				Body: fmt.Sprintf("%s is waiting · %s in", t.Title, in)})
		}
	} else if !s.DriftAt.IsZero() {
		t.DriftMin += int(n.Sub(s.DriftAt).Minutes())
		s.DriftAt = time.Time{}
	}
	return evs
}

func offerTitle(t *Task) string {
	switch t.Kind {
	case "resolve":
		return "Re-solve due: from scratch, no hints"
	case "reentry":
		return "Welcome back: a 10-minute warm-up"
	}
	return "Next problem is ready"
}

func briefing(s *State) string {
	got, need := s.today()
	due := 0
	for _, p := range s.Probes {
		if !p.Due.After(now()) {
			due++
		}
	}
	b := fmt.Sprintf("Streak %d · today %d/%d · week %d/%d", s.streak(), got, need, s.creditsSince(weekStart(now())), weeklyTarget)
	if due > 0 {
		b += fmt.Sprintf("\n%d re-solve(s) due", due)
	}
	if g := s.gapDays(); g >= 2 {
		b += fmt.Sprintf("\n%d days away. Today starts with an easy warm-up.", g)
	}
	return b
}

// poll checks the judges for new submissions on the current problem.
func poll(s *State, t *Task) []event {
	if t.Source() == "cf" {
		subs, err := cfSubs(20)
		if err != nil {
			log.Println("poll cf:", err)
			return nil
		}
		return processCF(s, t, subs)
	}
	_, recent, err := lcProfile()
	if err != nil {
		log.Println("poll lc:", err)
		return nil
	}
	return processLC(s, t, recent)
}

func processCF(s *State, t *Task, subs []cfSub) []event {
	var mine []cfSub
	for _, x := range subs {
		if x.Problem.key() == t.ID && x.Time > t.SubSeen && x.Time >= t.Assigned.Unix()-60 {
			mine = append(mine, x)
		}
	}
	slices.SortFunc(mine, func(a, b cfSub) int { return int(a.Time - b.Time) })
	var evs []event
	for _, x := range mine {
		if x.Verdict == "" || x.Verdict == "TESTING" {
			break // judge again next poll
		}
		t.SubSeen = x.Time
		at := time.Unix(x.Time, 0)
		begin(t, at)
		if x.Verdict == "OK" {
			return append(evs, acceptedEvent(s, t, at))
		}
		t.Fails++
		e := event{Key: fmt.Sprintf("fail:%s:%d", t.ID, t.Fails),
			Title: fmt.Sprintf("%s on test %d", verdictName(x.Verdict), x.Passed+1), Body: t.Title}
		if lvl, _ := hintAllowance(t); t.Fails >= 2 && lvl > t.Hints {
			e.Body += fmt.Sprintf("\n%d misses. A hint is available.", t.Fails)
			e.Actions = []action{{"hint", "Get hint"}, {"keep", "Keep going"}}
		}
		evs = append(evs, e)
	}
	return evs
}

func processLC(s *State, t *Task, recent []lcAC) []event {
	for _, a := range recent {
		ts, _ := strconv.ParseInt(a.Time, 10, 64)
		if "lc:"+a.Slug == t.ID && ts >= t.Assigned.Unix()-60 {
			return []event{acceptedEvent(s, t, time.Unix(ts, 0))}
		}
	}
	return nil
}

func acceptedEvent(s *State, t *Task, at time.Time) event {
	accept(s, t, at)
	got, need := s.today()
	body := fmt.Sprintf("%s · %s", t.Title, t.Finished.Sub(t.Started).Round(time.Minute))
	if t.Focus != "" {
		body += "\ntopic: " + t.Focus
	}
	body += fmt.Sprintf("\ntoday %d/%d", got, need)
	if got >= need {
		body += " · Claude unlocked"
	}
	acts := []action{{"explain", "Explain-back (2 min)"}}
	if s.wantsWork() {
		acts = append(acts, action{"next", "Next problem"})
	}
	return event{Key: "ac:" + t.ID, Title: "Accepted (" + t.Outcome + ")", Body: body, Actions: acts}
}

func verdictName(v string) string {
	switch v {
	case "WRONG_ANSWER":
		return "Wrong answer"
	case "TIME_LIMIT_EXCEEDED":
		return "TLE"
	case "MEMORY_LIMIT_EXCEEDED":
		return "MLE"
	case "RUNTIME_ERROR":
		return "Runtime error"
	case "COMPILATION_ERROR":
		return "Compilation error"
	}
	return strings.ReplaceAll(strings.ToLower(v), "_", " ")
}

// onAction runs a notification button.
func onAction(key string) {
	var launch func()
	err := update(func(s *State) error {
		switch key {
		case "start", "next":
			s.Snooze = time.Time{}
			if s.Current == nil {
				if _, err := assign(s); err != nil {
					return err
				}
			}
			t := s.Current
			begin(t, now())
			launch = func() { launchWork(t) }
		case "snooze":
			s.Snooze = now().Add(reOffer)
		case "rest":
			s.Snooze = day(now()).AddDate(0, 0, 1)
		case "hint":
			launch = func() { launchClaude("/edge-stuck") }
		case "teach":
			launch = func() { launchClaude("/edge-giveup") }
		case "explain":
			launch = func() { launchClaude("/edge-done") }
		}
		return nil
	})
	if err != nil {
		log.Println("action", key, err)
		return
	}
	if launch != nil {
		launch()
	}
}

func daemon() {
	log.SetFlags(0)
	log.Println("edge daemon up")
	acts := make(chan string, 8)
	var last emberStatus
	var lastSent time.Time
	for {
		var evs []event
		var st emberStatus
		if err := update(func(s *State) error {
			evs = step(s, focused())
			st = edgeStatus(s)
			return nil
		}); err != nil {
			log.Println("tick:", err)
		}
		// resend now and then too: Ember forgets everything when Noctalia restarts
		if st != last || time.Since(lastSent) > time.Minute {
			if toEmber(map[string]any{"type": "status", "status": st}) {
				last, lastSent = st, time.Now()
			}
		}
		for _, e := range evs {
			log.Printf("say %s: %s", e.Key, e.Title)
			if !toEmber(map[string]any{"type": "card", "card": emberCardFor(e)}) {
				notify(e, acts) // Ember isn't running: desktop notification instead
			}
		}
		select {
		case k := <-acts:
			log.Println("action", k)
			onAction(k)
		case <-time.After(tickEvery):
		}
	}
}
