package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
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
	Chips   []emberButton `json:"chips,omitempty"` // Ember v2.1: choice chips
	Urgent  bool          `json:"urgent,omitempty"`
	Bounce  bool          `json:"bounce,omitempty"`
	TTL     int           `json:"ttl_s,omitempty"` // seconds its buttons stay valid (cards with buttons)
}

type emberStatus struct {
	Today     int         `json:"today"`
	Need      int         `json:"need"`
	Streak    int         `json:"streak"`
	Closed    bool        `json:"closed"`
	Task      string      `json:"task,omitempty"`
	URL       string      `json:"url,omitempty"`
	StartedMs int64       `json:"startedMs,omitempty"`
	Pending   bool        `json:"pending,omitempty"`
	Round     *emberRound `json:"round,omitempty"` // a round that's close, live, or just over

	// Contract v2 (fix/base): all optional, old adapters ignore them.
	TaskID      string      `json:"taskId,omitempty"`      // "cf:2277C"; actions name it: start:<id>, skip:<id> …
	Short       string      `json:"short,omitempty"`       // pill label: "2277C", "LC 3542", "ABC482 D"
	Kind        string      `json:"kind,omitempty"`        // new | resolve | reentry | upsolve | self
	WorkSec     int         `json:"workSec,omitempty"`     // focused work on the current task
	TodaySec    int         `json:"todaySec,omitempty"`    // focused work today (this day only)
	Working     bool        `json:"working,omitempty"`     // work is being counted right now
	Paused      bool        `json:"paused,omitempty"`      // started, but no work for a while: show frozen
	AsOfMs      int64       `json:"asOfMs,omitempty"`      // when WorkSec was measured
	Policy      string      `json:"policy,omitempty"`      // ladder | instant | cold | ""
	HintsReady  bool        `json:"hintsReady,omitempty"`  // a hint can be taken now
	NextHintSec int         `json:"nextHintSec,omitempty"` // focused seconds until the next hint, 0 = none coming
	GiveUpInSec int         `json:"giveUpInSec"`           // focused seconds until give-up unlocks; 0 = now; -1 = never
	CanSkip     bool        `json:"canSkip,omitempty"`     // skip:<id> is accepted
	DueMs       int64       `json:"dueMs,omitempty"`       // upsolves: when it lapses
	Lapsed      bool        `json:"lapsed,omitempty"`      // an upsolve past its due date
	Judging     string      `json:"judging,omitempty"`     // "" | "test 9" | "queue": a submission being judged
	LastVerdict string      `json:"lastVerdict,omitempty"` // "WA on test 3" for the latest rejected submission
	Owed        []emberOwed `json:"owed,omitempty"`        // upsolves still owed, oldest first
	Explain     string      `json:"explain,omitempty"`     // task id waiting for explain-back (finished ≤ 12h ago)
	Debrief     string      `json:"debrief,omitempty"`     // round id waiting for its debrief
	Health      string      `json:"health,omitempty"`      // one plain sentence when a judge is down
	SeenMs      int64       `json:"seenMs,omitempty"`      // daemon heartbeat; the adapter treats > 90 s old as stopped

	// Contract v2.1 (v2/base): the trials. All optional.
	Phase      string `json:"phase,omitempty"`      // baseline | trial1 | trial2
	CueMs      int64  `json:"cueMs,omitempty"`      // tonight's rep time, once set
	RepDone    bool   `json:"repDone,omitempty"`    // today's rep counts
	Chain      int    `json:"chain,omitempty"`      // days in the chain (one miss doesn't break it)
	Missed     int    `json:"missed,omitempty"`     // misses in a row right now: 0 or 1 (2 breaks the chain)
	SkipTokens int    `json:"skipTokens,omitempty"` // left this week
	DeadlineMs int64  `json:"deadlineMs,omitempty"` // the rep's interview countdown ends then
	Pattern    string `json:"pattern,omitempty"`    // the current task's interview pattern
	Drill      bool   `json:"drill,omitempty"`      // the current task is the weekly drill
	ReadyPct   int    `json:"readyPct,omitempty"`   // readiness, 0..100
	ReadyLo    int    `json:"readyLo,omitempty"`    // its 80% interval
	ReadyHi    int    `json:"readyHi,omitempty"`
}

// emberOwed is one round that still owes upsolves.
type emberOwed struct {
	Round   string   `json:"round"`   // round id; upsolve:<round> starts its next one
	Short   string   `json:"short"`   // "CF 1129"
	Indexes []string `json:"indexes"` // ["C","D1"]
	DueMs   int64    `json:"dueMs"`
}

// emberRound is the round the island shows: from two hours before it starts
// until half an hour after it settles.
type emberRound struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Short    string         `json:"short"`
	URL      string         `json:"url"`
	StartMs  int64          `json:"startMs"`
	EndMs    int64          `json:"endMs"`
	Plan     string         `json:"plan"`             // in | out | "" (open)
	Joined   string         `json:"joined,omitempty"` // live | virtual
	Locked   bool           `json:"locked,omitempty"` // Claude is off for it
	Settled  bool           `json:"settled,omitempty"`
	Problems []emberProblem `json:"problems,omitempty"`
}

type emberProblem struct {
	Index string `json:"index"`
	State string `json:"state,omitempty"` // "" | tried | solved | upsolve | upsolved | taught | dropped | lapsed
}

// islandRound picks the round worth the island's attention now: a live one
// they're in, else the next one they're held to within two hours, else one
// they sat that settled in the last half hour.
func (s *State) islandRound() *Round {
	n := now()
	if r := s.liveHeld(); r != nil {
		return r
	}
	if r := s.nextHeld(); r != nil && r.Joined == "" && r.Start.Sub(n) <= 2*time.Hour {
		return r
	}
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if r := &s.Rounds[i]; r.Joined != "" && !r.Settled.IsZero() && n.Sub(r.Settled) < 30*time.Minute {
			return r
		}
	}
	return nil
}

// delivery is what became of one message to Ember.
type delivery int

const (
	emberDown    delivery = iota // no socket: Ember isn't running
	emberRefused                 // Ember answered ok:false: no edge plugin running to take it
	emberOK                      // the edge plugin got it (or an old Ember that doesn't answer)
)

// emberAckWait is how long edge waits for Ember's one-line answer.
const emberAckWait = 300 * time.Millisecond

func emberSock() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ember.sock")
}

// sendEmber delivers one message over Ember's socket and reads its answer:
// {"ok":true} when the edge plugin got it, {"ok":false} when none is
// running. No answer within emberAckWait is an older Ember: delivered.
func sendEmber(msg any) delivery {
	payload, _ := json.Marshal(msg)
	line, _ := json.Marshal(map[string]any{"type": "edge", "payload": json.RawMessage(payload)})
	c, err := net.DialTimeout("unix", emberSock(), 300*time.Millisecond)
	if err != nil {
		return emberDown
	}
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := c.Write(append(line, '\n')); err != nil {
		return emberDown
	}
	c.SetReadDeadline(time.Now().Add(emberAckWait))
	ans, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return emberOK
	}
	var ack struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(ans, &ack) == nil && ack.OK != nil && !*ack.OK {
		return emberRefused
	}
	return emberOK
}

// toEmber delivers one message; false means Ember or its edge plugin isn't there.
func toEmber(msg any) bool { return sendEmber(msg) == emberOK }

// taskMark is what the island's task cards depend on: which task, started or not.
type taskMark struct {
	id     string
	active bool
}

func markOf(t *Task) taskMark {
	if t == nil {
		return taskMark{}
	}
	return taskMark{t.ID, t.Active()}
}

func statusMark(st emberStatus) taskMark {
	return taskMark{st.TaskID, st.TaskID != "" && !st.Pending}
}

// taskSlots are the cards about the current task: they go when it ends.
var taskSlots = []string{"offer", "hint", "cold", "giveup", "drift", "fail"}

// afterChange follows a change made outside the daemon (an action, a
// command): the fresh status goes to Ember at once, and cards about a task
// that ended, or an offer for one now started, are taken down.
func afterChange(before, after taskMark, st emberStatus) {
	sendEmber(map[string]any{"type": "status", "status": st})
	var gone []string
	switch {
	case before.id != "" && before.id != after.id:
		gone = taskSlots
	case !before.active && after.active:
		gone = []string{"offer"}
	}
	if !st.Closed {
		gone = append(gone, "late")
	}
	for _, id := range gone {
		sendEmber(map[string]any{"type": "remove", "id": id})
	}
}

// heldCard is a card edge keeps: delivered with buttons (live, resent after
// Ember restarts) or refused (pending, resent when the plugin is back).
type heldCard struct {
	card emberCard
	key  string    // the event's key, marked said once delivered
	bind string    // what it's about; the card goes once that's over (cardValid)
	at   time.Time // first sent, for its ttl
}

func (h heldCard) expired(n time.Time) bool {
	ttl := h.card.TTL
	if ttl == 0 {
		ttl = 600
	}
	return n.Sub(h.at) >= time.Duration(ttl)*time.Second
}

// emberLink is the daemon's side of the island: the status it last sent and
// the cards still worth showing, so a card isn't lost to an Ember (or edge
// plugin) restart and a stale button never acts on the next task.
type emberLink struct {
	ok       bool   // the last message was delivered
	last     []byte // the status as statusKey sees it: no clock fields
	anchor   int64  // the running clock's start, in ms
	lastSent time.Time
	live     map[string]heldCard // by card id
	pending  map[string]heldCard
}

// cardBind says what an event's card is about, from its key.
func cardBind(e event) string {
	parts := strings.Split(e.Key, ":")
	id := ""
	if len(parts) >= 3 {
		id = parts[1] + ":" + parts[2]
	}
	switch e.slot() {
	case "offer":
		return "offer:" + id
	case "hint", "cold", "giveup", "drift", "fail":
		return "task:" + id
	case "ac":
		return "explain:" + id
	case "late":
		return "late"
	}
	return ""
}

// cardValid: the thing a card is about still holds in status st.
func cardValid(bind string, st emberStatus) bool {
	kind, id, _ := strings.Cut(bind, ":")
	switch kind {
	case "offer":
		return st.TaskID == id && st.Pending
	case "task":
		return st.TaskID == id
	case "explain":
		return st.Explain == id
	case "late":
		return st.Closed
	}
	return true
}

func (l *emberLink) send(msg any) delivery {
	d := sendEmber(msg)
	back := d == emberOK && !l.ok
	l.ok = d == emberOK
	if back {
		l.resend()
	}
	return d
}

// resend is for an Ember that's back: it forgot everything, so the status
// goes again and so do the live cards, with what's left of their ttl.
// Pending cards follow in tick, where they're marked said.
func (l *emberLink) resend() {
	l.last = nil
	n := time.Now()
	for id, h := range l.live {
		if h.expired(n) {
			delete(l.live, id)
			continue
		}
		c := h.card
		if c.TTL > 0 {
			c.TTL = max(1, c.TTL-int(n.Sub(h.at).Seconds()))
		}
		if sendEmber(map[string]any{"type": "card", "card": c}) != emberOK {
			return
		}
	}
}

// tick shows this tick's status and cards. It returns the keys of the
// events that reached him (on the island, or as a desktop notification when
// Ember isn't running at all): only those count as said.
func (l *emberLink) tick(st emberStatus, evs []event, acts chan<- string) (said []string) {
	if l.live == nil {
		l.live, l.pending = map[string]heldCard{}, map[string]heldCard{}
	}
	n := time.Now()
	// resend now and then too: Ember forgets everything when it restarts
	// a running clock alone isn't a change: Ember counts it from the anchor
	if b, anchor := statusKey(st); !slices.Equal(b, l.last) || abs64(anchor-l.anchor) > 3000 || n.Sub(l.lastSent) > time.Minute {
		if l.send(map[string]any{"type": "status", "status": st}) == emberOK {
			l.last, l.anchor, l.lastSent = b, anchor, n
		}
	}
	// cards about something that's over come down (pending ones were never shown)
	for id, h := range l.live {
		if !cardValid(h.bind, st) {
			sendEmber(map[string]any{"type": "remove", "id": id})
			delete(l.live, id)
		} else if h.expired(n) {
			delete(l.live, id)
		}
	}
	for id, h := range l.pending {
		if !cardValid(h.bind, st) || h.expired(n) {
			delete(l.pending, id)
		}
	}
	// refused cards whose plugin is back count as said once delivered
	if l.ok {
		for id, h := range l.pending {
			if sendEmber(map[string]any{"type": "card", "card": h.card}) == emberOK {
				said = append(said, h.key)
				delete(l.pending, id)
				if len(h.card.Buttons)+len(h.card.Chips) > 0 {
					l.live[id] = h
				}
			}
		}
	}
	for _, e := range evs {
		c := emberCardFor(e)
		h := heldCard{card: c, key: e.Key, bind: cardBind(e), at: n}
		switch l.send(map[string]any{"type": "card", "card": c}) {
		case emberOK:
			delete(l.pending, c.ID)
			if len(c.Buttons)+len(c.Chips) > 0 { // chip cards too: they come down with their task
				l.live[c.ID] = h
			} else {
				delete(l.live, c.ID)
			}
		case emberRefused:
			l.pending[c.ID] = h // the island is up, its edge plugin isn't: hold it
			log.Printf("ember has no edge plugin; holding %s", e.Key)
			continue
		default:
			if !notify(e, acts) {
				log.Printf("couldn't say %s; will retry", e.Key)
				continue
			}
		}
		said = append(said, e.Key)
	}
	for _, k := range said {
		log.Printf("said %s", k)
		logEvent("shown", map[string]any{"key": k})
	}
	for _, e := range evs {
		if e.Detail != "" && slices.Contains(said, e.Key) {
			log.Printf("%s: %s", e.Key, e.Detail) // the raw error: for the journal, not the island
		}
	}
	return said
}

func edgeStatus(s *State) emberStatus {
	n := now()
	got, need := s.today()
	st := emberStatus{Today: got, Need: need, Streak: s.streak(), Closed: s.closed(), GiveUpInSec: -1,
		TodaySec: s.workOn(day(n)), SeenMs: n.UnixMilli()}
	if r := s.islandRound(); r != nil && cfg().Rounds {
		er := &emberRound{ID: r.ID, Name: r.Name, Short: r.Short(), URL: r.RoundURL(), StartMs: r.Start.UnixMilli(), EndMs: r.End().UnixMilli(),
			Plan: r.Plan, Joined: r.Joined, Locked: s.roundLock() == r, Settled: !r.Settled.IsZero()}
		for _, p := range r.Problems {
			er.Problems = append(er.Problems, emberProblem{Index: p.Index, State: p.State})
		}
		st.Round = er
	}
	if t := s.Current; t != nil {
		switch t.Source() {
		case "lc":
			st.Task = t.Title
		case "ac":
			st.Task = strings.ToUpper(acContest(t.ID)) + " " + indexOf(t.ID) + " · " + t.Title
		default:
			st.Task = strings.TrimPrefix(t.ID, "cf:") + " · " + t.Title
		}
		st.URL, st.Pending = t.URL, !t.Active()
		if t.Active() {
			st.StartedMs = t.Started.UnixMilli()
		}
		st.TaskID, st.Short, st.Kind, st.WorkSec, st.Policy = t.ID, short(t), t.Kind, t.WorkSec, t.Policy
		st.Working, st.Paused = workState(s, t)
		st.Judging, st.LastVerdict = t.Judging, t.Verdict
		lvl, next := hintAllowance(t)
		st.HintsReady = lvl > t.Hints
		if next > 0 {
			st.NextHintSec = int(next.Seconds())
		}
		if t.Active() {
			_, wait := canGiveUp(t)
			st.GiveUpInSec = int(wait.Seconds())
		}
		st.CanSkip = canSkip(t)
		if p := s.upsolveOf(t); p != nil {
			st.DueMs = p.Due.UnixMilli()
			st.Lapsed = p.State == "lapsed" || !p.Due.After(n)
		}
	}
	for i := range s.Rounds {
		r := &s.Rounds[i]
		o := emberOwed{Round: r.ID, Short: r.Short()}
		for _, p := range r.Problems {
			if p.State == "upsolve" && p.Due.After(n) {
				o.Indexes = append(o.Indexes, p.Index)
				if o.DueMs == 0 || p.Due.UnixMilli() < o.DueMs {
					o.DueMs = p.Due.UnixMilli()
				}
			}
		}
		if len(o.Indexes) > 0 {
			st.Owed = append(st.Owed, o)
		}
	}
	if t := s.lastSolved(); t != nil && n.Sub(t.Finished) < 12*time.Hour {
		st.Explain = t.ID
	}
	if r := s.debriefRound(); r != nil && r.Lesson == "" && n.Sub(r.Settled) < debriefFor {
		st.Debrief = r.ID
	}
	if ks := s.troublesDue(); len(ks) > 0 {
		st.Health = plainTrouble(s, ks[0])
	}
	st.Phase = s.phase(n)
	consistencyStatus(s, &st)
	readinessStatus(s, &st)
	runtimeStatus(s, &st) // working, paused, the clock's anchor, judging: for CLI pushes too
	return st
}

// debriefFor: a round sat this recently and not debriefed still asks for it.
const debriefFor = 7 * 24 * time.Hour

// troublesDue are the troubles worth saying now, in order: a judge or a
// sync blips, so those only once they've lasted.
func (s *State) troublesDue() []string {
	var ks []string
	for _, k := range slices.Sorted(mapsKeys(s.Trouble)) {
		wait := time.Duration(0)
		if k == "cf" || k == "lc" || k == "ac" || strings.HasPrefix(k, "sync-") || strings.HasPrefix(k, "cal-") {
			wait = troubleAfter
		}
		if now().Sub(s.Trouble[k].Since) >= wait {
			ks = append(ks, k)
		}
	}
	return ks
}

// plainTrouble says what a trouble means, in one sentence for the island.
// The raw error (URLs, the handle, Go's wording) stays in the journal.
func plainTrouble(s *State, k string) string {
	src, rest, _ := strings.Cut(k, "-")
	switch {
	case srcName(k) != "":
		return srcName(k) + " isn't answering · verdicts may be late · edge keeps trying"
	case src == "sync" && srcName(rest) != "":
		return "Can't sync your " + srcName(rest) + " history · edge keeps trying"
	case src == "cal" && srcName(rest) != "":
		return "Can't fetch the " + srcName(rest) + " contest calendar · edge keeps trying"
	case k == "assign":
		return "edge can't pick a problem right now"
	case k == "config":
		return "config.toml has lines edge ignores · `edge config` lists them"
	case k == "gate":
		return "The Claude gate is off · run `edge install`"
	}
	return s.Trouble[k].Msg
}

// cardTTL is how long a card's buttons stay valid, by slot.
var cardTTL = map[string]int{"offer": 1800, "hint": 1800, "cold": 1800, "giveup": 1800, "ac": 3600, "late": 3600,
	"roundeve": 3600, "roundsoon": 900, "roundlate": 1800, "roundover": 3600, "roundmissed": 3600}

// emberCardFor turns a brain event into an Ember card, primary action last.
func emberCardFor(e event) emberCard {
	mood := map[string]string{"offer": "work", "hint": "nudge", "cold": "nudge", "giveup": "nudge", "drift": "nudge",
		"late": "error", "ac": "happy", "fail": "error", "lapsed": "nudge",
		"roundeve": "work", "roundsoon": "work", "roundlate": "nudge", "rounddrift": "nudge", "roundover": "happy",
		"roundmissed": "nudge", "rated": "happy", "cue": "work", "cuenudge": "nudge"}[e.slot()]
	if mood == "" {
		mood = "idle"
	}
	c := emberCard{ID: e.slot(), Mood: mood, Title: e.Title, Body: e.Body, Urgent: e.Urgent, Bounce: e.slot() == "ac"}
	primary := map[string]bool{"start": true, "open": true, "next": true, "hint": true, "teach": true, "giveup": true,
		"explain": true, "upsolve": true, "register": true, "openround": true}
	var lead []emberButton
	for _, a := range e.Actions {
		verb, _, _ := strings.Cut(a.Key, ":")
		b := emberButton{Key: a.Key, Label: a.Label, Primary: primary[verb]}
		if b.Primary {
			lead = append(lead, b)
		} else {
			c.Buttons = append(c.Buttons, b)
		}
	}
	c.Buttons = append(c.Buttons, lead...)
	for _, a := range e.Chips {
		c.Chips = append(c.Chips, emberButton{Key: a.Key, Label: a.Label})
	}
	if len(c.Buttons) > 0 || len(c.Chips) > 0 {
		c.TTL = cardTTL[c.ID]
		if c.TTL == 0 {
			c.TTL = 600
		}
	}
	return c
}
