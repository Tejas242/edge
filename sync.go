package main

import (
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
)

// Network I/O never runs under the state lock: the gate hook and Ember's
// buttons share it. The pattern is: read a snapshot, fetch, then apply the
// results in a short update that re-checks the state.

const (
	syncEvery    = 6 * time.Hour
	fullEvery    = 24 * time.Hour // the whole CF history; otherwise the last 200
	troubleAfter = 30 * time.Minute
)

// backoff is the wait after n failures in a row: 1m, 2m, 4m ... 1h.
func backoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	return min(time.Hour, time.Minute<<min(n-1, 6))
}

func (s *State) leg(src string) *syncLeg {
	if s.Sync[src] == nil {
		s.Sync[src] = &syncLeg{}
	}
	return s.Sync[src]
}

func (s *State) syncDue(src string) bool {
	l := s.leg(src)
	n := now()
	every := syncEvery
	if s.awaitingRating(src) {
		every = 30 * time.Minute // a round's rating change is due
	}
	if j, ok := strings.CutPrefix(src, "cal-"); ok && slices.ContainsFunc(s.Rounds, func(r Round) bool {
		return r.Judge() == j && r.held() && r.Start.After(n) && r.Start.Sub(n) < 3*time.Hour
	}) {
		every = 20 * time.Minute // a held round is close: catch a delay
	}
	return n.Sub(l.OK) >= every && n.Sub(l.Try) >= backoff(l.Fails)
}

// syncData is one source's fetched profile.
type syncData struct {
	Src      string
	Err      error
	Rating   int
	CFSubs   []cfSub
	Full     bool
	LCRecent []lcAC
	History  []RatingPoint // contest rating history; nil = not fetched
}

func fetchSync(src string, full bool) syncData {
	d := syncData{Src: src, Full: full}
	if src == "cf" {
		if d.Rating, d.Err = cfRating(); d.Err != nil {
			return d
		}
		n := 200
		if full {
			n = 0
		}
		if d.CFSubs, d.Err = cfSubs(n); d.Err != nil {
			return d
		}
		d.History, _ = cfRatingHistory() // a chart, not worth failing the sync for
		return d
	}
	if src == "ac" {
		d.Rating, d.History, d.Err = acHistory()
		return d
	}
	d.Rating, d.History, d.LCRecent, d.Err = lcProfile()
	return d
}

func (s *State) applySync(d syncData) {
	l := s.leg(d.Src)
	l.Try = now()
	if d.Err != nil {
		l.Fails++
		log.Printf("sync %s: %v (retry in %s)", d.Src, d.Err, backoff(l.Fails))
		if now().Sub(l.OK) > 12*time.Hour {
			s.trouble("sync-"+d.Src, fmt.Sprintf("can't sync %s: %v", srcName(d.Src), d.Err))
		}
		return
	}
	l.OK, l.Fails = now(), 0
	s.fixed("sync-" + d.Src)
	if d.Full {
		l.Full = now()
	}
	s.setRating(d.Src, d.Rating)
	if d.History != nil {
		s.RatingLog[d.Src] = d.History
	}
	for _, x := range d.CFSubs {
		k := x.Problem.key()
		s.CFTried[k] = true
		if x.Verdict == "OK" {
			if at, ok := s.CFDone[k]; !ok || x.Time < at {
				s.CFDone[k] = x.Time
			}
		}
	}
	for _, a := range d.LCRecent {
		s.Seen["lc:"+a.Slug] = true
	}
	s.virtuals(d.CFSubs)
	s.rateRounds()
}

// setRating takes a new live rating. The adaptive offset exists to cover the
// gap until the rating catches up, so a rise eats a positive offset (and a
// drop a negative one) instead of counting the same progress twice.
func (s *State) setRating(src string, r int) {
	if r <= 0 {
		return
	}
	old := s.Ratings[src]
	s.Ratings[src] = r
	if old == 0 {
		return
	}
	switch d, o := r-old, s.Offset[src]; {
	case o > 0 && d > 0:
		s.Offset[src] = max(0, o-d)
	case o < 0 && d < 0:
		s.Offset[src] = min(0, o-d)
	}
}

// applyCal takes a judge's calendar, with the same backoff as a sync.
func (s *State) applyCal(c calendar) {
	l := s.leg("cal-" + c.Judge)
	l.Try = now()
	if c.Err != nil {
		l.Fails++
		log.Printf("calendar %s: %v (retry in %s)", c.Judge, c.Err, backoff(l.Fails))
		if now().Sub(l.OK) > 24*time.Hour {
			s.trouble("cal-"+c.Judge, fmt.Sprintf("can't read %s's contest calendar: %v", srcName(c.Judge), c.Err))
		}
		return
	}
	l.OK, l.Fails = now(), 0
	s.fixed("cal-" + c.Judge)
	s.applyCalendar(c.Judge, c.Rounds)
}

func srcName(src string) string {
	return map[string]string{"cf": "Codeforces", "lc": "LeetCode", "ac": "AtCoder"}[src]
}

// catalog is the problem lists a pick needs, loaded outside the lock.
type catalog struct {
	cf, lc []Problem
	err    map[string]error
}

func loadCatalog() *catalog {
	c := &catalog{err: map[string]error{}}
	for _, src := range cfg().Sources {
		var err error
		if src == "cf" {
			c.cf, err = cfProblems()
		} else {
			c.lc, err = lcProblems()
		}
		if err != nil {
			c.err[src] = err
		}
	}
	if c.cf == nil && c.err["cf"] == nil {
		c.cf, c.err["cf"] = cfProblems() // warm-ups are always Codeforces
	}
	return c
}

func (c *catalog) probs(src string) []Problem {
	if c == nil {
		return nil
	}
	if src == "cf" {
		return c.cf
	}
	return c.lc
}

// judged is a fetch of the judge's recent submissions for the current task.
type judged struct {
	ID     string // the task it was fetched for
	Err    error
	CF     []cfSub
	LC     []lcSub
	AC     []acSub
	Polled time.Time
}

func fetchJudge(t *Task) *judged {
	j := &judged{ID: t.ID, Polled: now()}
	switch t.Source() {
	case "cf":
		j.CF, j.Err = cfSubs(20)
	case "ac":
		j.AC, j.Err = acSubs(t.Assigned.Add(-time.Minute))
	default:
		j.LC, j.Err = lcSubs()
	}
	return j
}

// syncSources is every profile edge syncs: the problem sources, and
// AtCoder when its rounds are followed.
func syncSources() []string {
	srcs := []string{"cf", "lc"}
	if c := cfg(); c.Rounds && slices.Contains(c.RoundJudges, "ac") && c.ACUser != "" {
		srcs = append(srcs, "ac")
	}
	return srcs
}

// gathered is everything one tick fetched before taking the lock.
type gathered struct {
	syncs  []syncData
	cat    *catalog
	judge  *judged
	gateOK *bool // nil: not checked this tick
	cals   []calendar
	obs    []roundObs
}

// calendar is one judge's fetched contest list.
type calendar struct {
	Judge  string
	Rounds []calRound
	Err    error
}

// gather does the tick's network I/O, deciding from a lock-free snapshot.
func gather(snap *State) gathered {
	var g gathered
	for _, src := range syncSources() {
		if snap.syncDue(src) {
			g.syncs = append(g.syncs, fetchSync(src, src == "cf" && now().Sub(snap.leg("cf").Full) >= fullEvery))
		}
	}
	if cfg().Rounds {
		for _, j := range cfg().RoundJudges {
			if snap.syncDue("cal-" + j) {
				cal, err := fetchCalendar(j)
				g.cals = append(g.cals, calendar{Judge: j, Rounds: cal, Err: err})
			}
		}
		for _, r := range snap.observeDue() {
			g.obs = append(g.obs, observe(r, now()))
		}
	}
	if snap.wantsAssign() {
		g.cat = loadCatalog()
	}
	if t := snap.Current; t != nil && now().Sub(snap.LastPoll) >= pollEvery {
		g.judge = fetchJudge(t)
	}
	return g
}
