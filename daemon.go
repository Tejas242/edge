package main

import (
	"bytes"
	"fmt"
	"log"
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
}

// slot groups events so a newer one replaces the older on screen.
func (e event) slot() string { return strings.SplitN(e.Key, ":", 2)[0] }

type window struct {
	ID         uint64 // niri's window id, for the per-window cwd cache
	App, Title string
	PID        int // the window's process, for terminals
}

var terminals = []string{"alacritty", "kitty", "foot", "ghostty", "wezterm", "konsole", "terminal", "xterm"}

// judgeWords mark a judge's pages in a browser title. AtCoder's task pages
// don't name AtCoder ("D - Takahashi the Wall Breaker"): the task's own
// title catches its page, "atcoder" the contest, submission and editorial
// pages.
var judgeWords = []string{"codeforces", "leetcode", "atcoder", "cp-algorithms", "usaco.guide"}

// working: does the screen say they're on the problem? The problem or a
// judge in the browser, a terminal whose programs (shell, editor, Claude)
// are working inside the practice workspace, or a solution file saved in the
// last few minutes. A terminal on another project doesn't count.
func working(w window, t *Task, fileFresh bool, ws string) bool {
	if fileFresh {
		return true
	}
	if isTermApp(w.App) {
		return w.PID > 0 && cwdsUnder(procCwds(w.PID), ws)
	}
	return judgeTab(w, senseTask{Title: t.Title})
}

func isTermApp(app string) bool {
	app = strings.ToLower(app)
	return slices.ContainsFunc(terminals, func(term string) bool { return strings.Contains(app, term) })
}

func cwdsUnder(cwds []string, ws string) bool {
	return slices.ContainsFunc(cwds, func(d string) bool { return under(d, ws) })
}

// judgeTab: a browser window on a judge, or on the task itself.
func judgeTab(w window, t senseTask) bool {
	if isTermApp(w.App) {
		return false
	}
	title := strings.ToLower(w.Title)
	if slices.ContainsFunc(judgeWords, func(k string) bool { return strings.Contains(title, k) }) {
		return true
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

// stale: offered on an earlier day, a few times, never started.
func (s *State) stale() bool {
	t := s.Current
	return t != nil && !t.Active() && t.Offers >= 2 && day(t.Assigned).Before(day(now()))
}

// step is one daemon tick under the lock: apply what gather fetched, track
// work, judge. Returns what to say. No network. dt is the time since the
// last step, capped (drift accounting); work itself comes from the sensor.
func step(s *State, w window, g gathered, dt time.Duration) []event {
	dt = max(0, min(dt, 2*idleTick))
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
	if len(confErrs) > 0 {
		s.trouble("config", "config.toml: "+strings.Join(confErrs, "; "))
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
		if at, ok := sense.fileTime(solutionPath(t)); ok {
			if !t.Active() && at.After(t.Assigned) {
				begin(s, t, at) // Competitive Companion / nvim created the file: they've started
			}
			if t.Active() && at.After(t.Started) {
				t.Edited = true
			}
		}
		if j := g.judge; j != nil && j.ID == t.ID {
			if j.Err != nil {
				s.trouble(t.Source(), fmt.Sprintf("can't reach %s to check your submissions: %v", srcName(t.Source()), j.Err))
			} else {
				s.fixed(t.Source())
				t.Judging = ""
				switch t.Source() {
				case "cf":
					evs = append(evs, processCF(s, t, j.CF)...)
				case "ac":
					evs = append(evs, processAC(s, t, j.AC)...)
				default:
					evs = append(evs, processLC(s, t, j.LC)...)
				}
				liveJudge = judgeNote{t.ID, t.Judging}
			}
		}
	}
	trackWork(s, w, dt)
	if t := s.Current; t != nil && liveJudge.id == t.ID && t.Judging == "" {
		t.Judging = liveJudge.text // runtime only: carried between ticks
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
		if t := s.Current; t != nil && k == "offer:"+t.ID {
			t.Offers++
		}
	}
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
	if live == nil && (n.Hour() >= 7 || n.Sub(day(n)) >= 24*time.Hour) {
		evs = append(evs, event{Key: "brief:" + date, Title: "edge · today", Body: briefing(s)})
	}
	if n.Hour() >= 22 && s.closed() && live == nil {
		evs = append(evs, event{Key: "late:" + date, Urgent: true, Title: fmt.Sprintf("%d task(s) left today", need-got),
			Body: "Claude stays locked outside " + tilde(s.practiceDir()) + " until it's done."})
	}
	for _, k := range slices.Sorted(mapsKeys(s.Trouble)) {
		tr := s.Trouble[k]
		wait := time.Duration(0)
		if k == "cf" || k == "lc" || k == "ac" || strings.HasPrefix(k, "sync-") || strings.HasPrefix(k, "cal-") {
			wait = troubleAfter // the judges blip; say so only when it lasts
		}
		if n.Sub(tr.Since) >= wait {
			body := tr.Msg
			if s.failOpen() != "" && (k == "assign" || k == "cf" || k == "lc") {
				body += "\nThe gate is open until this is fixed."
			}
			evs = append(evs, event{Key: "health:" + k + ":" + date, Title: "edge needs a look", Body: body})
		}
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
		if studyTime(n) && !n.Before(s.Snooze) {
			acts := []action{{"start", "Start"}, {"snooze", "In 30 min"}}
			if got >= need {
				acts = append(acts, action{"rest", "Done for today"})
			}
			evs = append(evs, event{Key: "offer:" + t.ID, Every: cfg().OfferEvery, Title: offerTitle(t),
				Body: fmt.Sprintf("%s · rating %d\nToday %d/%d · streak %d", t.Title, t.Rating, got, need, s.streak()), Actions: acts})
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
					Body: t.Title, Actions: []action{{"hint", "Get hint"}, {"keep", "Keep going"}}})
				break
			}
		}
	case "instant":
		// the same moment ladder's first hint unlocks: the arms differ only in what's allowed
		if t.Hints == 0 && el >= cfg().LadderAt[0] {
			evs = append(evs, event{Key: "hint:" + t.ID + ":i1", Title: fmt.Sprintf("%s in · want a nudge?", in),
				Body: t.Title, Actions: []action{{"hint", "Get hint"}, {"keep", "Keep going"}}})
		}
	case "cold":
		if el >= 30*time.Minute {
			evs = append(evs, event{Key: "cold:" + t.ID, Title: fmt.Sprintf("%s into the re-solve", in),
				Body:    "No hints on re-solves. Giving up brings it back later, with the solution taught now.",
				Actions: []action{{"teach", "Give up & learn it"}, {"keep", "Keep going"}}})
		}
	}
	if ok, _ := canGiveUp(t); ok && el >= cfg().GiveUpAt && t.Policy != "cold" {
		evs = append(evs, event{Key: "giveup:" + t.ID, Title: fmt.Sprintf("%s on %s", in, t.Title),
			Body:    "Honest work counts for today either way.",
			Actions: []action{{"teach", "Teach me the solution"}, {"keep", "Keep going"}}})
	}

	if re := cfg().Distraction; re != nil && re.MatchString(w.Title) {
		t.DriftSec += int(dt.Seconds())
		if s.DriftAt.IsZero() {
			s.DriftAt = n
		} else if n.Sub(s.DriftAt) >= cfg().DriftAfter {
			evs = append(evs, event{Key: "drift:" + t.ID, Every: 10 * time.Minute, Title: fmt.Sprintf("You're on %s", re.FindString(w.Title)),
				Body: fmt.Sprintf("%s is waiting · %s of work in", t.Title, in)})
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

func briefing(s *State) string {
	got, need := s.today()
	due := 0
	for _, p := range s.Probes {
		if !p.Due.After(now()) {
			due++
		}
	}
	b := fmt.Sprintf("Streak %d · today %d/%d · week %d/%d", s.streak(), got, need, s.creditsSince(weekStart(now())), cfg().Weekly)
	if due > 0 {
		b += fmt.Sprintf("\n%d re-solve(s) due", due)
	}
	if g := s.gapDays(); g >= 2 {
		b += fmt.Sprintf("\n%d days away. Today starts with an easy warm-up.", g)
	}
	if r := s.nextHeld(); r != nil && r.Start.Sub(now()) < 7*24*time.Hour {
		b += fmt.Sprintf("\nNext round: %s, %s", r.Short(), clockDay(r.Start))
	}
	if r, p := s.nextUpsolve(); p != nil {
		b += fmt.Sprintf("\nUpsolve %s %s by %s", r.Short(), p.Index, p.Due.Add(-time.Minute).Format("Mon"))
	}
	return b
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
			t.Judging = "queue"
			if x.Verdict == "TESTING" {
				t.Judging = fmt.Sprintf("test %d", x.Passed+1)
			}
			break // judge again next poll
		}
		t.SubSeen = x.Time
		at := time.Unix(x.Time, 0)
		begin(s, t, at)
		if x.Verdict == "OK" {
			accept(s, t, at)
			return evs // plan says "Accepted"
		}
		t.Verdict = cfVerdict(x)
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
		t.Verdict = lcVerdict(x.Status)
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
		if j := acJudging(x.Result); j != "" {
			t.Judging = j
			break
		}
		t.SubSeen = x.Time
		at := time.Unix(x.Time, 0)
		begin(s, t, at)
		if x.Result == "AC" {
			accept(s, t, at)
			return evs
		}
		t.Verdict = x.Result
		evs = append(evs, failEvent(t, acVerdictName(x.Result)))
	}
	return evs
}

// acJudging is a submission AtCoder is still judging: "queue" while it
// waits, "test 4" when "3/14" are done. "" = a final result.
func acJudging(res string) string {
	if res == "WJ" || res == "WR" {
		return "queue"
	}
	done, total, ok := strings.Cut(res, "/")
	if !ok {
		return ""
	}
	d, err1 := strconv.Atoi(strings.TrimSpace(done))
	n, err2 := strconv.Atoi(strings.TrimSpace(total))
	if err1 != nil || err2 != nil || n <= 0 {
		return "queue"
	}
	return fmt.Sprintf("test %d", min(d+1, n))
}

// cfVerdict is a rejection as the island says it: "WA on test 3".
func cfVerdict(x cfSub) string {
	short := map[string]string{"WRONG_ANSWER": "WA", "TIME_LIMIT_EXCEEDED": "TLE", "MEMORY_LIMIT_EXCEEDED": "MLE",
		"RUNTIME_ERROR": "RE", "IDLENESS_LIMIT_EXCEEDED": "ILE", "OUTPUT_LIMIT_EXCEEDED": "OLE"}[x.Verdict]
	switch {
	case x.Verdict == "COMPILATION_ERROR":
		return "CE"
	case short == "":
		return verdictName(x.Verdict)
	}
	return fmt.Sprintf("%s on test %d", short, x.Passed+1)
}

// lcVerdict shortens LeetCode's status; its public list has no test number.
func lcVerdict(status string) string {
	if v := map[string]string{"Wrong Answer": "WA", "Time Limit Exceeded": "TLE", "Memory Limit Exceeded": "MLE",
		"Runtime Error": "RE", "Compile Error": "CE", "Output Limit Exceeded": "OLE"}[status]; v != "" {
		return v
	}
	return status
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
		e.Actions = []action{{"hint", "Get hint"}, {"keep", "Keep going"}}
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

// onAction runs a notification or Ember button. A repeat of the same key
// within a few seconds (a double click, two notifications) is dropped.
func onAction(key string) {
	var cat *catalog
	if key == "start" || key == "next" {
		if snap, err := load(); err == nil && snap.Current == nil {
			cat = loadCatalog() // outside the lock
		}
	}
	var launch func()
	err := update(func(s *State) error {
		if s.LastAct == key && now().Sub(s.LastActAt) < 3*time.Second {
			return nil
		}
		s.LastAct, s.LastActAt = key, now()
		switch key {
		case "start", "next":
			s.Snooze = time.Time{}
			if s.Current == nil {
				if cat == nil {
					return fmt.Errorf("no problem lists loaded")
				}
				if _, err := assign(s, cat); err != nil {
					return err
				}
			}
			t := *s.Current
			begin(s, s.Current, now())
			launch = func() {
				if err := scaffold(&t); err != nil {
					log.Println("scaffold:", err)
				}
				launchWork(&t)
			}
		case "snooze":
			s.Snooze = now().Add(cfg().OfferEvery)
		case "rest":
			s.Snooze = day(now()).AddDate(0, 0, 1).Add(cfg().DayEnds)
		case "upsolve":
			if t := s.Current; t != nil && t.Kind != "upsolve" {
				if t.Active() {
					return fmt.Errorf("finish %s first; the upsolve is next", t.Title)
				}
				s.Current = nil // unstarted: it comes back another day
			}
			if s.Current == nil {
				if _, p := s.nextUpsolve(); p == nil {
					return fmt.Errorf("no upsolve waiting")
				}
				if _, err := assign(s, nil); err != nil {
					return err
				}
			}
			t := *s.Current
			begin(s, s.Current, now())
			launch = func() {
				if err := scaffold(&t); err != nil {
					log.Println("scaffold:", err)
				}
				launchWork(&t)
			}
		case "keep":
		case "debrief":
			launch = func() { launchClaude("/edge-round") }
		case "hint":
			launch = func() { launchClaude("/edge-stuck") }
		case "teach":
			launch = func() { launchClaude("/edge-giveup") }
		case "explain":
			launch = func() { launchClaude("/edge-done") }
		default:
			verb, id, _ := strings.Cut(key, ":")
			r := s.round(id)
			if r == nil {
				return fmt.Errorf("no round %q", id)
			}
			switch verb {
			case "register":
				u := r.regURL()
				launch = func() { spawn(workDir(), cfg().Browser, u) }
			case "openround":
				rr := *r
				launch = func() { launchRound(&rr) }
			case "skipround":
				if r.Joined == "live" {
					return fmt.Errorf("you've already submitted in %s: you're in it", r.Short())
				}
				r.Plan = "out"
				log.Println("can't make it:", r.ID)
			}
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

// daemon is edge's loop. It steps on a tick (20 s while a task is started or
// a round is near, 60 s otherwise), when a gather lands, after an action, and
// 3 s after the sensor reports a change (work starting or stopping, the
// problem file arriving, a submit). Network I/O runs in its own goroutine,
// one gather at a time, so a stalled judge never holds the loop or the clock.
func daemon() {
	log.SetFlags(0)
	log.Println("edge daemon up")
	sense = newSensor()
	sense.start(workDir())
	acts := make(chan string, 8)
	landed := make(chan gathered, 1)
	var (
		g                                    gathered
		gathering                            bool
		jp                                   judgePlan
		lastKey                              []byte
		lastAnchor                           int64
		lastSent, brokenSaid, lastStep, seen time.Time
		sw                                   statsWriter
		next                                 = busyTick
	)
	for {
		if time.Since(seen) >= busyTick {
			touch(daemonSeenPath())
			seen = time.Now()
		}
		ok := gateInstalled()
		g.gateOK = &ok
		w := sense.window()
		var dt time.Duration
		if !lastStep.IsZero() {
			dt = time.Since(lastStep)
		}
		lastStep = time.Now()
		polled := g.judge != nil && g.judge.Err == nil
		var (
			evs   []event
			st    emberStatus
			stats *Stats
			snap  *State
		)
		if err := update(func(s *State) error {
			evs = step(s, w, g, dt)
			st = edgeStatus(s)
			runtimeStatus(s, &st)
			x := s.stats()
			stats = &x
			snap = s // not touched after update returns: a safe snapshot for planning
			return nil
		}); err != nil {
			log.Println("tick:", err)
			if snap == nil && time.Since(brokenSaid) > time.Hour {
				// no state and no backup: say so, at most hourly
				notify(event{Key: "health:state", Title: "edge is stopped", Body: err.Error(), Urgent: true}, acts)
				brokenSaid = time.Now()
			}
		}
		g = gathered{}
		if stats != nil {
			sw.publish(*stats)
		}
		// push on a real change, and every minute as the heartbeat (Ember
		// forgets everything when Noctalia restarts)
		key, anchor := statusKey(st)
		if !bytes.Equal(key, lastKey) || abs64(anchor-lastAnchor) > 3000 || time.Since(lastSent) > time.Minute {
			if toEmber(map[string]any{"type": "status", "status": st}) {
				lastKey, lastAnchor, lastSent = key, anchor, time.Now()
			}
		}
		var said []string
		for _, e := range evs {
			if toEmber(map[string]any{"type": "card", "card": emberCardFor(e)}) || notify(e, acts) {
				log.Printf("said %s: %s", e.Key, e.Title)
				said = append(said, e.Key)
			} else {
				log.Printf("couldn't say %s; will retry", e.Key)
			}
		}
		if len(said) > 0 {
			if err := update(func(s *State) error { s.markSaid(said); return nil }); err != nil {
				log.Println("mark said:", err)
			}
		}
		n := time.Now()
		if snap != nil {
			if polled {
				jp.polled(st.Judging, n)
			}
			if t := snap.Current; t != nil && t.Active() {
				jp.signal(sense.submitted(), n)
			}
			next = tickFor(snap)
			if !gathering {
				var lastWork time.Time
				if t := snap.Current; t != nil {
					lastWork = t.WorkAt
				}
				if p := planFetch(snap, jp.due(snap.Current, lastWork, n)); !p.empty() {
					if p.judge != nil {
						jp.last = n
					}
					gathering = true
					go func() { landed <- p.gather(snap) }()
				}
			}
		}
		d := next
		if !gathering { // in flight: its landing wakes the loop
			d = max(time.Second, jp.wait(next, n))
		}
		timer := time.NewTimer(d)
		var settle <-chan time.Time
	wait:
		for {
			select {
			case k := <-acts:
				log.Println("action", k)
				onAction(k)
				break wait
			case g = <-landed:
				gathering = false
				break wait
			case <-timer.C:
				break wait
			case <-sense.wake:
				if settle == nil {
					settle = time.After(flipDelay)
				}
			case <-settle:
				break wait
			}
		}
		timer.Stop()
	}
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
