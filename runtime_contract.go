package main

import (
	"encoding/json"
	"time"
)

// workState says whether work on t is being counted right now, and whether a
// started task should show as paused (no work for a while, or across the day
// boundary). In the daemon the sensor answers live; elsewhere (edge action,
// edge stats) it's work counted in the last ~40 s.
func workState(s *State, t *Task) (working, paused bool) {
	if t == nil || !t.Active() {
		return false, false
	}
	n := now()
	if on, ok := sense.workingNow(t.ID); ok {
		working = on
	} else {
		working = !t.WorkAt.IsZero() && n.Sub(t.WorkAt) <= workingFor
	}
	if working {
		return true, false
	}
	last := t.Started
	if t.WorkAt.After(last) {
		last = t.WorkAt
	}
	return false, n.Sub(last) >= pausedAfter || day(last).Before(day(n))
}

// trackWork adds the sensor's counted work to the current task. Outside the
// daemon (no sensor) it falls back to judging the focused window per step.
func trackWork(s *State, w window, dt time.Duration) {
	t := s.Current
	if t == nil {
		sense.take(senseTask{}, w)
		return
	}
	st := senseTask{ID: t.ID, Title: t.Title, Count: t.Active() && s.liveHeld() == nil, // a round's time isn't the task's
		Path: solutionPath(t), WS: s.practiceDir()}
	if sense != nil {
		sec, at, _ := sense.take(st, w)
		if st.Count && sec > 0 {
			t.WorkSec += sec
		}
		if st.Count && at.After(t.WorkAt) && at.After(t.Started) {
			t.WorkAt = at
		}
		return
	}
	if !st.Count {
		return
	}
	fresh := false
	if at, ok := sense.fileTime(st.Path); ok {
		fresh = now().Sub(at) < editFresh
	}
	if working(w, t, fresh, st.WS) {
		t.WorkSec += int(dt.Seconds())
		t.WorkAt = now()
	}
}

// judgeNote is the live judging state of the last poll; Task.Judging isn't
// persisted, so the daemon carries it between ticks.
type judgeNote struct{ id, text string }

var liveJudge judgeNote

// runtimeStatus fills the status fields the runtime owns: Working, Paused,
// AsOfMs, WorkSec, Judging. AsOfMs moves only while work is counted, so an
// idle status stays byte-identical.
func runtimeStatus(s *State, st *emberStatus) {
	t := s.Current
	if t == nil {
		return
	}
	st.WorkSec = t.WorkSec
	st.Working, st.Paused = workState(s, t)
	st.Judging = t.Judging
	if t.Verdict != "" {
		st.LastVerdict = t.Verdict
	}
	switch {
	case st.Working:
		st.AsOfMs = now().UnixMilli()
	case !t.WorkAt.IsZero():
		st.AsOfMs = t.WorkAt.UnixMilli()
	case t.Active():
		st.AsOfMs = t.Started.UnixMilli()
	}
}

// statusKey is what makes a status worth pushing: everything except the
// heartbeat, and a running clock only when its start (asOf − workSec)
// moves.
func statusKey(st emberStatus) (key []byte, anchorMs int64) {
	if st.Working {
		anchorMs = st.AsOfMs - int64(st.WorkSec)*1000
		st.WorkSec = 0
	}
	st.AsOfMs, st.SeenMs = 0, 0
	key, _ = json.Marshal(st)
	return key, anchorMs
}

// judgePlan decides when the judge is asked about the current task: a burst
// (every 3 s, up to 40) after a submit signal or while a submission is being
// judged, a trickle (every minute) while a started task saw work in the last
// 10 min, and never otherwise.
type judgePlan struct {
	last   time.Time // the last poll started
	left   int       // polls left in the burst; 0 = no burst
	made   int       // polls made in this burst
	ended  time.Time // when the last burst ended
	submit time.Time // the submit signal already acted on
}

func (j *judgePlan) due(t *Task, lastWork, n time.Time) bool {
	switch {
	case t == nil:
		return false
	case j.left > 0:
		return n.Sub(j.last) >= burstEvery
	}
	return t.Active() && !lastWork.IsZero() && n.Sub(lastWork) <= trickleFor && n.Sub(j.last) >= pollEvery
}

// signal is a judge-tab title change: start a burst, unless one just ran
// (browsing the judge shouldn't keep it busy).
func (j *judgePlan) signal(at, n time.Time) {
	if !at.After(j.submit) {
		return
	}
	j.submit = at
	if j.left == 0 && n.Sub(j.ended) >= 15*time.Second {
		j.left, j.made = burstMax, 0
	}
}

// polled takes a finished poll: a submission still being judged keeps (or
// starts) a burst; a burst that finds nothing pending stops after a few polls.
func (j *judgePlan) polled(judging string, n time.Time) {
	if j.left == 0 {
		if judging != "" {
			j.left, j.made = burstMax, 0 // a trickle poll saw TESTING
		}
		return
	}
	j.left--
	j.made++
	if judging == "" && j.made >= burstGrace {
		j.left = 0
	}
	if j.left == 0 {
		j.ended = n
	}
}

// wait is how long until the next poll is due, if sooner than d.
func (j *judgePlan) wait(d time.Duration, n time.Time) time.Duration {
	if j.left > 0 {
		return max(0, min(d, burstEvery-n.Sub(j.last)))
	}
	return d
}

const (
	busyTick = 20 * time.Second // a task is started or a round is near
	idleTick = 60 * time.Second // otherwise
)

// tickFor is the daemon's cadence: fast while there's work to time or a
// round to follow, slow otherwise (the sensor's events wake it anyway).
func tickFor(s *State) time.Duration {
	n := now()
	if t := s.Current; t != nil && t.Active() {
		return busyTick
	}
	if s.liveHeld() != nil {
		return busyTick
	}
	if r := s.nextHeld(); r != nil && r.Start.Sub(n) < time.Hour {
		return busyTick
	}
	return idleTick
}
