package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ActionKeys is every key Ember's buttons and rows (or a desktop
// notification) may send to `edge action <key>`. <id> is a task id
// ("cf:2277C"), <round> a round id ("cf:2272"). A key that names a task or
// round is checked against it: a stale one is an error, never an action on
// whatever is current now. TestEveryActionKeyIsHandled keeps this list and
// act in step; CONTRACT.md has what each one does.
var ActionKeys = []string{
	"start", "start:<id>", "open", "open:<id>", "next",
	"skip:<id>", "giveup:<id>", "teach", "hint", "hint:<id>",
	"upsolve", "upsolve:<round>",
	"explain", "explain:<id>", "debrief", "debrief:<round>",
	"snooze", "rest", "keep",
	"register:<round>", "openround:<round>", "skipround:<round>", "undo:skipround:<round>",
	"cue:now", "cue:<HH:MM>", "skiptonight", // Trial 1 (consistency.go)
	"mistake:<id>:<kind>", "drill", "drill:later", // Trial 2 (readiness.go)
}

// skipWithin: a new problem can be skipped from Ember until this much work.
// Past it, giving up (which can count) is the way out.
const skipWithin = 10 * time.Minute

// canSkip: skip:<id> is accepted for t.
func canSkip(t *Task) bool {
	return t != nil && (t.Kind == "upsolve" || t.Kind == "new" && t.Worked() < skipWithin)
}

// short is a task's label on the island: "2277C", "LC 3542", "ABC482 D".
func short(t *Task) string {
	switch t.Source() {
	case "lc":
		if n, _, ok := strings.Cut(t.Title, ". "); ok && n != "" && strings.Trim(n, "0123456789") == "" {
			return "LC " + n
		}
		return t.Title
	case "ac":
		return strings.ToUpper(acContest(t.ID)) + " " + indexOf(t.ID)
	}
	return strings.TrimPrefix(t.ID, "cf:")
}

// shortID labels a task id that may no longer be current.
func shortID(id string) string { return short(&Task{ID: id}) }

// onAction runs a key for the daemon (a desktop notification's button):
// errors go to the journal.
func onAction(key string) {
	if msg, err := act(key); err != nil {
		log.Printf("action %s: %v", key, err)
	} else if msg != "" {
		log.Printf("action %s: %s", key, msg)
	}
}

// act runs one action key and says what happened in a line, or why it
// didn't in a plain sentence. Right after a change it pushes the fresh
// status to Ember, so the island follows the click at once. A repeat of the
// same key within a few seconds (a double click) does nothing.
func act(key string) (string, error) {
	verb, arg, _ := strings.Cut(key, ":")
	var cat *catalog
	if verb == "start" || verb == "open" || verb == "next" || verb == "skip" {
		if snap, err := load(); err == nil && (snap.Current == nil || !snap.Current.Active() || verb == "skip") {
			cat = catalogFn() // network, outside the lock
		}
	}
	var (
		msg    string
		launch func()
		st     emberStatus
		before taskMark
		undo   *emberCard
		buf    bytes.Buffer
	)
	old := out
	out = &buf // skip and friends print; act says one line instead
	err := update(func(s *State) error {
		before = markOf(s.Current)
		if s.LastAct == key && now().Sub(s.LastActAt) < 3*time.Second {
			return errDouble
		}
		s.LastAct, s.LastActAt = key, now()
		s.tidyRounds() // a lapsed upsolve isn't startable
		// cur is the current task, checked against the key's id when it names one
		cur := func() (*Task, error) {
			t := s.Current
			switch {
			case t == nil && arg != "":
				return nil, fmt.Errorf("%s isn't your problem any more", shortID(arg))
			case t == nil:
				return nil, errors.New("there's no problem right now")
			case arg != "" && arg != t.ID:
				return nil, fmt.Errorf("that was for %s; you're on %s now", shortID(arg), short(t))
			}
			return t, nil
		}
		work := func(t *Task) {
			begin(s, t, now())
			c := *t
			launch = func() {
				if err := scaffold(&c); err != nil {
					log.Println("scaffold:", err)
				}
				launchWork(&c)
			}
		}
		switch verb {
		case "start", "open", "next":
			if verb == "next" && arg != "" {
				return fmt.Errorf("unknown action %q", key)
			}
			s.Snooze = time.Time{}
			if s.Current == nil && arg == "" {
				if cat == nil {
					return errors.New("edge has no problem lists loaded; try again in a minute")
				}
				if _, err := assign(s, cat); err != nil {
					return fmt.Errorf("couldn't pick a problem: %v", err)
				}
			}
			t, err := cur()
			if err != nil {
				return err
			}
			work(t)
			msg = "Working on " + short(t)
		case "skip":
			t, err := cur()
			if err != nil {
				return err
			}
			if arg == "" {
				return fmt.Errorf("unknown action %q", key)
			}
			if !canSkip(t) {
				if t.Kind == "new" {
					return fmt.Errorf("%s of work in: give up & learn instead, it can count", dur(t.Worked()))
				}
				return errors.New("re-solves and warm-ups can't be skipped; giving up brings it back later")
			}
			if err := skip(s, false, cat); err != nil {
				return err
			}
			logEvent("skip", map[string]any{"task": t.ID, "kind": t.Kind, "work_sec": t.WorkSec, "via": "ember"})
			msg = "Skipped " + short(t)
		case "giveup", "teach":
			if verb == "giveup" && arg == "" {
				return fmt.Errorf("unknown action %q", key)
			}
			if verb == "teach" && arg != "" {
				return fmt.Errorf("unknown action %q", key)
			}
			t, err := cur()
			if err != nil {
				return err
			}
			if !t.Active() {
				return fmt.Errorf("start %s first", short(t))
			}
			if ok, wait := canGiveUp(t); !ok {
				return fmt.Errorf("give up unlocks after %s more work", dur(wait))
			}
			launch = func() { launchClaude("/edge-giveup") }
			msg = "Opening Claude to teach " + short(t)
		case "hint":
			t, err := cur()
			if err != nil {
				return err
			}
			if lvl, wait := hintAllowance(t); lvl <= t.Hints {
				switch {
				case t.Policy == "cold":
					return errors.New("no hints on this one: it's from scratch")
				case !t.Active():
					return fmt.Errorf("start %s first", short(t))
				case t.Hints >= 3:
					return errors.New("all 3 hints are used")
				default:
					return fmt.Errorf("the next hint unlocks after %s more work", dur(wait))
				}
			}
			launch = func() { launchClaude("/edge-stuck") }
			msg = "Opening Claude for a hint on " + short(t)
		case "upsolve":
			var r *Round
			var p *RoundProblem
			if arg == "" {
				if t := s.Current; t != nil && t.Kind == "upsolve" {
					work(t)
					msg = "Upsolving " + short(t)
					break
				}
				if r, p = s.nextUpsolve(); p == nil {
					return errors.New("no upsolve is owed right now")
				}
			} else {
				if r = s.round(arg); r == nil {
					return fmt.Errorf("edge doesn't know the round %s", arg)
				}
				if t := s.Current; t != nil && t.Kind == "upsolve" && t.Round == r.ID {
					work(t)
					msg = "Upsolving " + short(t)
					break
				}
				if p = r.owed(now()); p == nil {
					return fmt.Errorf("%s owes no upsolve now", r.Short())
				}
			}
			if t := s.Current; t != nil {
				if t.Active() {
					return fmt.Errorf("finish %s first; the upsolve is next", short(t))
				}
				putBack(s, now()) // unstarted: it comes back, nothing lost
			}
			t := assignUpsolve(s, r, p)
			work(t)
			msg = "Upsolving " + short(t)
		case "explain":
			t := s.lastSolved()
			switch {
			case t == nil:
				return errors.New("nothing is waiting for an explain-back")
			case arg != "" && arg != t.ID:
				return fmt.Errorf("the explain-back waiting is for %s, not %s", short(t), shortID(arg))
			}
			launch = func() { launchClaude("/edge-done") }
			msg = "Opening Claude for the explain-back on " + short(t)
		case "debrief":
			r := s.debriefRound()
			switch {
			case r == nil:
				return errors.New("no round to debrief yet")
			case arg != "" && arg != r.ID:
				return fmt.Errorf("the round to debrief is %s now", r.Short())
			}
			launch = func() { launchClaude("/edge-round") }
			msg = "Opening Claude to debrief " + r.Short()
		case "snooze", "rest", "keep":
			if arg != "" {
				return fmt.Errorf("unknown action %q", key)
			}
			switch verb {
			case "snooze":
				s.Snooze = now().Add(cfg().OfferEvery)
				msg = "Asking again at " + s.Snooze.Format("15:04")
			case "rest":
				s.Snooze = day(now()).AddDate(0, 0, 1).Add(cfg().DayEnds)
				msg = "Done for today"
			default:
				msg = "Keep going"
			}
		case "register", "openround", "skipround":
			r := s.round(arg)
			if r == nil {
				return fmt.Errorf("edge doesn't know the round %q", arg)
			}
			switch verb {
			case "register":
				u := r.regURL()
				launch = func() { spawn(workDir(), cfg().Browser, u) }
				msg = "Opening " + r.Short() + "'s registration"
			case "openround":
				rr := *r
				launch = func() { launchRound(&rr) }
				msg = "Opening " + r.Short()
			case "skipround":
				if r.Joined == "live" {
					return fmt.Errorf("you've already submitted in %s: you're in it", r.Short())
				}
				if r.Plan != "out" {
					r.PlanWas = r.Plan
					if r.PlanWas == "" {
						r.PlanWas = "open"
					}
				}
				r.Plan = "out"
				log.Println("can't make it:", r.ID)
				msg = "Out of " + r.Short()
				undo = &emberCard{ID: "undo", Mood: "idle", Title: "Out of " + r.Short(), TTL: 15,
					Buttons: []emberButton{{Key: "undo:skipround:" + r.ID, Label: "Undo"}}}
			}
		case "undo":
			what, id, _ := strings.Cut(arg, ":")
			r := s.round(id)
			if what != "skipround" || r == nil {
				return fmt.Errorf("unknown action %q", key)
			}
			if r.Plan != "out" || r.PlanWas == "" || !r.Settled.IsZero() {
				return fmt.Errorf("nothing to undo for %s", r.Short())
			}
			r.Plan, r.PlanWas = strings.TrimPrefix(r.PlanWas, "open"), ""
			log.Println("back in:", r.ID)
			msg = "Back in " + r.Short()
		default:
			ok, m, err := consistencyAction(s, verb, arg)
			if !ok {
				ok, m, err = readinessAction(s, verb, arg)
			}
			if !ok {
				return fmt.Errorf("unknown action %q", key)
			}
			if err != nil {
				return err
			}
			msg = m
			if t := s.Current; verb == "drill" && arg == "" && t != nil && t.Kind == "drill" {
				work(t) // the drill opens like any start
			}
		}
		st = edgeStatus(s)
		return nil
	})
	out = old
	if errors.Is(err, errDouble) {
		return "", nil
	}
	if err != nil {
		logEvent("action_error", map[string]any{"key": key, "err": err.Error()})
		return "", err
	}
	logEvent("action", map[string]any{"key": key})
	if launch != nil {
		launch()
	}
	afterChange(before, statusMark(st), st)
	if undo != nil {
		sendEmber(map[string]any{"type": "card", "card": *undo})
	}
	return msg, nil
}

var errDouble = errors.New("double click")

// catalogFn loads the problem lists; tests swap it so nothing goes online.
var catalogFn = loadCatalog
