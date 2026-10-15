package main

import (
	"fmt"
	"log"
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
	return n.Sub(l.OK) >= syncEvery && n.Sub(l.Try) >= backoff(l.Fails)
}

// syncData is one source's fetched profile.
type syncData struct {
	Src      string
	Err      error
	Rating   int
	CFSubs   []cfSub
	Full     bool
	LCRecent []lcAC
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
		d.CFSubs, d.Err = cfSubs(n)
		return d
	}
	d.Rating, d.LCRecent, d.Err = lcProfile()
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

func srcName(src string) string {
	return map[string]string{"cf": "Codeforces", "lc": "LeetCode"}[src]
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
	Polled time.Time
}

func fetchJudge(t *Task) *judged {
	j := &judged{ID: t.ID, Polled: now()}
	if t.Source() == "cf" {
		j.CF, j.Err = cfSubs(20)
	} else {
		j.LC, j.Err = lcSubs()
	}
	return j
}

// gathered is everything one tick fetched before taking the lock.
type gathered struct {
	syncs  []syncData
	cat    *catalog
	judge  *judged
	gateOK *bool // nil: not checked this tick
}

// gather does the tick's network I/O, deciding from a lock-free snapshot.
func gather(snap *State) gathered {
	var g gathered
	for _, src := range []string{"cf", "lc"} {
		if snap.syncDue(src) {
			g.syncs = append(g.syncs, fetchSync(src, src == "cf" && now().Sub(snap.leg("cf").Full) >= fullEvery))
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
