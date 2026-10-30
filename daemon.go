package main

import (
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	tickEvery = 20 * time.Second
	pollEvery = 60 * time.Second
	editFresh = 3 * time.Minute // a solution file saved this recently means they're working
)

type action struct{ Key, Label string }

type event struct {
	Key     string        // dedupe key: said once, or again after Every
	Every   time.Duration // 0 = once
	Title   string
	Body    string
	Urgent  bool
	Actions []action
	Detail  string // for the journal only (a raw error), never shown
}

// slot groups events so a newer one replaces the older on screen.
func (e event) slot() string { return strings.SplitN(e.Key, ":", 2)[0] }

type window struct {
	App, Title string
	PID        int // the window's process, for terminals
}

var terminals = []string{"alacritty", "kitty", "foot", "ghostty", "wezterm", "konsole", "terminal", "xterm"}

// working: does the screen say they're on the problem? The problem or a
// judge in the browser, a terminal whose programs (shell, editor, Claude)
// are working inside the practice workspace, or a solution file saved in the
// last few minutes. A terminal on another project doesn't count.
func working(w window, t *Task, fileFresh bool, ws string) bool {
	if fileFresh {
		return true
	}
	app, title := strings.ToLower(w.App), strings.ToLower(w.Title)
	for _, term := range terminals {
		if strings.Contains(app, term) {
			return w.PID > 0 && slices.ContainsFunc(procCwds(w.PID), func(d string) bool { return under(d, ws) })
		}
	}
	for _, k := range []string{"codeforces", "leetcode", "atcoder", "cp-algorithms", "usaco.guide"} {
		if strings.Contains(title, k) {
			return true
		}
	}
	return t.Title != "" && strings.Contains(title, strings.ToLower(t.Title))
}

func (s *State) wantsWork() bool {
	got, need := s.today()
	return got < need || s.creditsSince(weekStart(now())) < cfg().Weekly
}

// wantsAssign: the daemon should hand out a problem now (or replace one
// that sat unstarted since an earlier day).
func (s *State) wantsAssign() bool {
	n := now()
	if !studyTime(n) || n.Before(s.Snooze) || !s.wantsWork() {
		return false
	}
	return s.Current == nil || s.stale()
}

// stale: offered on an earlier day, a few times, never started. Upsolves
// never go stale: they keep their round's due date and lapse on it, so
// shelving one only brought it straight back.
func (s *State) stale() bool {
	t := s.Current
	return t != nil && !t.Active() && t.Kind != "upsolve" && t.Offers >= 2 && day(t.Assigned).Before(day(now()))
}

// step is one daemon tick under the lock: apply what gather fetched, track
// work, judge. Returns what to say. No network. dt is the time since the
// last tick, capped: a suspend or a stall isn't work.
func step(s *State, w window, g gathered, dt time.Duration) []event {
	n := now()
	dt = max(0, min(dt, 2*tickEvery))
	s.pin()
	for _, d := range g.syncs {
		s.applySync(d)
	}
	for _, c := range g.cals {
		s.applyCal(c)
	}
	for _, o := range g.obs {
		s.applyObs(o)
	}
	s.planRounds()
	s.tidyRounds()
	if g.gateOK != nil {
		if *g.gateOK {
			s.fixed("gate")
		} else {
			s.trouble("gate", "the edge hook is missing from ~/.claude/settings.json, or hooks are disabled: run `edge install`")
		}
	}
	if errs := configErrs(); len(errs) > 0 {
		s.trouble("config", "config.toml: "+strings.Join(errs, "; "))
	} else {
		s.fixed("config")
	}
	if s.stale() && g.cat != nil {
		log.Println("shelving unstarted", s.Current.ID)
		shelve(s)
	}
	if s.Current == nil && g.cat != nil && s.wantsAssign() {
		if t, err := assign(s, g.cat); err != nil {
			log.Println("assign:", err)
		} else {
			log.Println("assigned", t.ID)
		}
	}
	var evs []event
	if t := s.Current; t != nil {
		if st, err := os.Stat(solutionPath(t)); err == nil {
			if !t.Active() && st.ModTime().After(t.Assigned) {
				begin(s, t, st.ModTime()) // Competitive Companion / nvim created the file: they've started
			}
			if t.Active() && st.ModTime().After(t.Started) {
				t.Edited = true
			}
		}
		if j := g.judge; j != nil && j.ID == t.ID {
			s.LastPoll = j.Polled
			if j.Err != nil {
				s.trouble(t.Source(), fmt.Sprintf("can't reach %s to check your submissions: %v", srcName(t.Source()), j.Err))
			} else {
				s.fixed(t.Source())
				switch t.Source() {
				case "cf":
					evs = append(evs, processCF(s, t, j.CF)...)
				case "ac":
					evs = append(evs, processAC(s, t, j.AC)...)
				default:
					evs = append(evs, processLC(s, t, j.LC)...)
				}
			}
		}
	}
	if t := s.Current; t != nil && t.Active() && s.liveHeld() == nil { // a round's time isn't the task's
		fresh := false
		if st, err := os.Stat(solutionPath(t)); err == nil {
			fresh = n.Sub(st.ModTime()) < editFresh
		}
		if working(w, t, fresh, s.practiceDir()) {
			t.addWork(n, int(dt.Seconds()))
		}
	}
	evs = append(evs, plan(s, w, dt)...)
	return s.filter(evs)
}

// filter drops what was already said. Events are marked said only once
// delivered (markSaid), so a failed notification is tried again.
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
		out = append(out, e)
	}
	return out
}

// markSaid records delivered events; an offer shown counts toward shelving.
func (s *State) markSaid(keys []string) {
	for _, k := range keys {
		s.Said[k] = now()
		if t := s.Current; t != nil && strings.HasPrefix(k, "offer:"+t.ID+":") {
			t.Offers++
		}
	}
}

// offersADay: an unstarted task gets at most this many offer cards an
// evening; after that it lives on the pill alone.
const offersADay = 2

// offerKey is the next offer card for t today, or "" when today's are used:
// the first at once, the second OfferEvery after it.
func (s *State) offerKey(t *Task, date string) string {
	base := "offer:" + t.ID + ":" + date + ":"
	var last time.Time
	for i := 1; i <= offersADay; i++ {
		at, ok := s.Said[base+strconv.Itoa(i)]
		if !ok {
			if i > 1 && now().Sub(last) < cfg().OfferEvery {
				return ""
			}
			return base + strconv.Itoa(i)
		}
		last = at
	}
	return ""
}

// plan is the judgement: what's worth saying right now. No IO. dt is the
// time since the last tick, for drift accounting.
func plan(s *State, w window, dt time.Duration) []event {
	n := now()
	date := day(n).Format("2006-01-02")
	got, need := s.today()
	var evs []event

	// in a live round: the round and edge's health, nothing else
	live := s.liveHeld()
	// late: from 22:00 until the day ends (04:00), not just until midnight
	if n.Sub(day(n)) >= 22*time.Hour && s.closed() && live == nil {
		start := action{"next", "Start"}
		if t := s.Current; t != nil {
			start = action{"start:" + t.ID, "Start"}
			if t.Active() {
				start.Label = "Open"
			}
		}
		evs = append(evs, event{Key: "late:" + date, Urgent: true, Title: plural(need-got, "task") + " left today",
			Body: "Claude stays locked outside " + tilde(s.practiceDir()) + " until it's done.", Actions: []action{start}})
	}
	for _, k := range s.troublesDue() { // the judges blip: those only once they've lasted
		body := plainTrouble(s, k)
		if s.failOpen() != "" && (k == "assign" || k == "cf" || k == "lc" || k == "ac") {
			body += "\nThe gate is open until this is fixed."
		}
		evs = append(evs, event{Key: "health:" + k + ":" + date, Title: "edge needs a look", Body: body, Detail: s.Trouble[k].Msg})
	}
	evs = append(evs, s.roundEvents(w, dt)...)
	if live != nil {
		return evs
	}
	if t := s.lastSolved(); t != nil && n.Sub(t.Finished) < 12*time.Hour {
		evs = append(evs, acceptedEvent(s, t))
	}

	t := s.Current
	if t == nil || !t.Active() {
		if !s.DriftAt.IsZero() {
			s.DriftAt = time.Time{} // drift only means something mid-task
		}
	}
	if t == nil {
		return evs
	}
	if !t.Active() {
		if k := s.offerKey(t, date); k != "" && studyTime(n) && !n.Before(s.Snooze) {
			acts := []action{{"start:" + t.ID, "Start"}, {"snooze", "In 30 min"}}
			if got >= need {
				acts = append(acts, action{"rest", "Done for today"})
			}
			evs = append(evs, event{Key: k, Title: offerTitle(t), Body: offerBody(s, t), Actions: acts})
		}
		return evs
	}

	el := t.Worked()
	in := dur(el)
	switch t.Policy {
	case "ladder":
		for l := 3; l >= 1; l-- {
			if el >= cfg().LadderAt[l-1] && t.Hints < l {
				evs = append(evs, event{Key: fmt.Sprintf("hint:%s:%d", t.ID, l), Title: fmt.Sprintf("%s in · hint %d unlocked", in, l),
					Body: t.Title, Actions: []action{{"hint:" + t.ID, "Get hint"}, {"keep", "Keep going"}}})
				break
			}
		}
	case "instant":
		// the same moment ladder's first hint unlocks: the arms differ only in what's allowed
		if t.Hints == 0 && el >= cfg().LadderAt[0] {
			evs = append(evs, event{Key: "hint:" + t.ID + ":i1", Title: fmt.Sprintf("%s in · want a nudge?", in),
				Body: t.Title, Actions: []action{{"hint:" + t.ID, "Get hint"}, {"keep", "Keep going"}}})
		}
	case "cold":
		if el >= 30*time.Minute {
			evs = append(evs, event{Key: "cold:" + t.ID, Every: giveUpEvery, Title: fmt.Sprintf("%s into the re-solve", in),
				Body:    "No hints on re-solves. Giving up brings it back later, with the solution taught now.",
				Actions: []action{{"giveup:" + t.ID, "Give up & learn it"}, {"keep", "Keep going"}}})
		}
	}
	if ok, _ := canGiveUp(t); ok && el >= cfg().GiveUpAt && t.Policy != "cold" {
		evs = append(evs, event{Key: "giveup:" + t.ID, Every: giveUpEvery, Title: fmt.Sprintf("%s on %s", in, t.Title),
			Body:    "Honest work counts for today either way.",
			Actions: []action{{"giveup:" + t.ID, "Teach me the solution"}, {"keep", "Keep going"}}})
	}

	if re := cfg().Distraction; re != nil && re.MatchString(w.Title) {
		t.DriftSec += int(dt.Seconds())
		if s.DriftAt.IsZero() {
			s.DriftAt = n
		} else if n.Sub(s.DriftAt) >= cfg().DriftAfter {
			evs = append(evs, event{Key: "drift:" + t.ID, Every: 10 * time.Minute, Title: fmt.Sprintf("You're on %s", re.FindString(w.Title)),
				Body: fmt.Sprintf("%s is waiting · %s of work in", t.Title, in), Actions: []action{{"open:" + t.ID, "Back to it"}}})
		}
	} else {
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
	case "upsolve":
		return "Upsolve " + t.RoundName + ": the round's next problem"
	}
	return "Next problem is ready"
}

// giveUpEvery: the give-up and cold cards come back this often while they
// stay true, so missing one never leaves him without a way out.
const giveUpEvery = 30 * time.Minute

// offerBody is the offer's text: the problem, then the day. No "rating 0"
// for an upsolve CF hasn't rated, no "streak 0".
func offerBody(s *State, t *Task) string {
	got, need := s.today()
	b := t.Title
	if t.Rating > 0 {
		b += fmt.Sprintf(" · rating %d", t.Rating)
	}
	b += fmt.Sprintf("\nToday %d/%d", got, need)
	if k := s.streak(); k > 0 {
		b += fmt.Sprintf(" · streak %d", k)
	}
	if p := s.upsolveOf(t); p != nil {
		b += " · due " + p.Due.Add(-time.Minute).Format("Mon")
	}
	return b
}

// plural: "1 task", "2 tasks".
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
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
		begin(s, t, at)
		if x.Verdict == "OK" {
			accept(s, t, at)
			return evs // plan says "Accepted"
		}
		evs = append(evs, failEvent(t, fmt.Sprintf("%s on test %d", verdictName(x.Verdict), x.Passed+1)))
	}
	return evs
}

func processLC(s *State, t *Task, subs []lcSub) []event {
	var mine []lcSub
	for _, x := range subs {
		if "lc:"+x.Slug == t.ID && x.unix() > t.SubSeen && x.unix() >= t.Assigned.Unix()-60 {
			mine = append(mine, x)
		}
	}
	slices.SortFunc(mine, func(a, b lcSub) int { return int(a.unix() - b.unix()) })
	var evs []event
	for _, x := range mine {
		t.SubSeen = x.unix()
		at := time.Unix(x.unix(), 0)
		begin(s, t, at)
		if x.Status == "Accepted" {
			accept(s, t, at)
			return evs
		}
		evs = append(evs, failEvent(t, x.Status))
	}
	return evs
}

func processAC(s *State, t *Task, subs []acSub) []event {
	var mine []acSub
	for _, x := range subs {
		if "ac:"+x.Problem == t.ID && x.Time > t.SubSeen && x.Time >= t.Assigned.Unix()-60 {
			mine = append(mine, x)
		}
	}
	slices.SortFunc(mine, func(a, b acSub) int { return int(a.Time - b.Time) })
	var evs []event
	for _, x := range mine {
		if x.Result == "WJ" || x.Result == "WR" || strings.HasSuffix(x.Result, "/") {
			break
		}
		t.SubSeen = x.Time
		at := time.Unix(x.Time, 0)
		begin(s, t, at)
		if x.Result == "AC" {
			accept(s, t, at)
			return evs
		}
		evs = append(evs, failEvent(t, acVerdictName(x.Result)))
	}
	return evs
}

func acVerdictName(v string) string {
	if n := map[string]string{"WA": "Wrong answer", "TLE": "TLE", "MLE": "MLE", "RE": "Runtime error", "CE": "Compilation error", "OLE": "Output limit"}[v]; n != "" {
		return n
	}
	return v
}

func failEvent(t *Task, title string) event {
	t.Fails++
	e := event{Key: fmt.Sprintf("fail:%s:%d", t.ID, t.Fails), Title: title, Body: t.Title}
	if lvl, _ := hintAllowance(t); t.Fails >= 2 && lvl > t.Hints {
		e.Body += fmt.Sprintf("\n%d misses. A hint is available.", t.Fails)
		e.Actions = []action{{"hint:" + t.ID, "Get hint"}, {"keep", "Keep going"}}
	}
	return e
}

func acceptedEvent(s *State, t *Task) event {
	got, need := s.today()
	body := fmt.Sprintf("%s · %s of work", t.Title, dur(t.Worked()))
	if t.Focus != "" {
		body += "\ntopic: " + t.Focus
	}
	body += fmt.Sprintf("\ntoday %d/%d", got, need)
	if got >= need {
		body += " · Claude unlocked"
	}
	if t.Known {
		body += "\nThat fast, you'd seen it before: no re-solves for this one."
	}
	acts := []action{{"explain:" + t.ID, "Explain-back (2 min)"}}
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

func daemon() {
	log.SetFlags(0)
	log.Println("edge daemon up")
	acts := make(chan string, 8)
	var link emberLink
	var brokenSaid, lastTick time.Time
	var sw statsWriter
	for {
		touch(daemonSeenPath())
		snap, err := load()
		if err != nil {
			// no state and no backup: say so, at most hourly
			log.Println("state:", err)
			if time.Since(brokenSaid) > time.Hour {
				notify(event{Key: "health:state", Title: "edge is stopped", Body: err.Error(), Urgent: true}, acts)
				brokenSaid = time.Now()
			}
			time.Sleep(tickEvery)
			continue
		}
		g := gather(snap)
		ok := gateInstalled()
		g.gateOK = &ok
		w := focused()
		var evs []event
		var st emberStatus
		var stats *Stats
		var dt time.Duration
		if !lastTick.IsZero() {
			dt = time.Since(lastTick)
		}
		lastTick = time.Now()
		if err := update(func(s *State) error {
			evs = step(s, w, g, dt)
			st = edgeStatus(s)
			x := s.stats()
			stats = &x
			return nil
		}); err != nil {
			log.Println("tick:", err)
		}
		if stats != nil {
			sw.publish(*stats)
		}
		said := link.tick(st, evs, acts)
		if len(said) > 0 {
			if err := update(func(s *State) error { s.markSaid(said); return nil }); err != nil {
				log.Println("mark said:", err)
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
