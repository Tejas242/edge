package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Saturday 17 Oct 2026: a CF Div. 1+2 at 20:05, ABC at 17:30, a Sunday
// morning LeetCode weekly and a Monday Div. 2.
func calendarWeek() map[string][]calRound {
	sat := func(h, m int) time.Time { return time.Date(2026, 10, 17, h, m, 0, 0, time.Local) }
	return map[string][]calRound{
		"cf": {
			{ID: "cf:2261", Name: "WorldQuant Round 1 (Codeforces Round 1128, Div. 1 + Div. 2)", Start: sat(20, 5), Dur: 3 * time.Hour},
			{ID: "cf:2270", Name: "Codeforces Round 1129 (Div. 1)", Start: time.Date(2026, 10, 19, 20, 5, 0, 0, time.Local), Dur: 2 * time.Hour},
			{ID: "cf:2271", Name: "Codeforces Round 1129 (Div. 2)", Start: time.Date(2026, 10, 19, 20, 5, 0, 0, time.Local), Dur: 2 * time.Hour},
			{ID: "cf:2272", Name: "Kotlin Heroes: Episode 14", Start: time.Date(2026, 10, 20, 20, 5, 0, 0, time.Local), Dur: 2 * time.Hour},
			{ID: "cf:2280", Name: "Codeforces Round (Div. 3)", Start: time.Date(2026, 10, 21, 14, 35, 0, 0, time.Local), Dur: 2 * time.Hour},
		},
		"ac": {
			{ID: "ac:abc480", Name: "AtCoder Beginner Contest 480", Start: sat(17, 30), Dur: 100 * time.Minute},
			{ID: "ac:ahc073", Name: "AtCoder Heuristic Contest 073", Start: sat(11, 30), Dur: 4 * time.Hour},
		},
		"lc": {
			{ID: "lc:weekly-contest-524", Name: "Weekly Contest 524", Start: time.Date(2026, 10, 18, 8, 0, 0, 0, time.Local), Dur: 90 * time.Minute},
		},
	}
}

// thu is Thursday 15 Oct 2026, 21:00.
var thu = time.Date(2026, 10, 15, 21, 0, 0, 0, time.Local)

func withCalendar(s *State) {
	for _, j := range []string{"cf", "ac", "lc"} {
		s.applyCalendar(j, calendarWeek()[j])
	}
}

func plans(s *State) string {
	var b []string
	for _, r := range s.Rounds {
		p := r.Plan
		if p == "" {
			p = "open"
		}
		b = append(b, r.ID+"="+p)
	}
	return strings.Join(b, " ")
}

func TestFollowsOnlyRoundsRatedForThem(t *testing.T) {
	at(t, thu)
	s := fresh(t)
	withCalendar(s)
	var ids []string
	for _, r := range s.Rounds {
		ids = append(ids, r.ID+":"+r.Kind)
	}
	got := strings.Join(ids, " ")
	// sorted by start: ABC 17:30 Sat, CF 20:05 Sat, LC Sun morning, then next week
	want := "ac:abc480:abc cf:2261:div12 lc:weekly-contest-524:weekly cf:2271:div2 cf:2280:div3"
	if got != want {
		t.Fatalf("followed:\n got %s\nwant %s", got, want)
	}
	if s.round("cf:2261").Short() != "CF 1128" || s.round("ac:abc480").Short() != "ABC 480" || s.round("lc:weekly-contest-524").Short() != "Weekly 524" {
		t.Fatal("short names")
	}
	if (&Round{ID: "cf:1", Name: "Educational Codeforces Round 192 (Rated for Div. 2)", Kind: "edu"}).Short() != "Edu 192" {
		t.Fatal("edu short name")
	}
	if (&Round{ID: "cf:1", Name: "Codeforces Round (Div. 2)", Kind: "div2"}).Short() != "CF Div. 2" {
		t.Fatal("unnumbered round")
	}
}

func TestPlanHoldsTwoAWeekOnePerDayInStudyTime(t *testing.T) {
	at(t, thu)
	s := fresh(t)
	withCalendar(s)
	s.planRounds()
	// this week (Mon 12 - Sun 18): Codeforces first, one a day, so Saturday's
	// ABC stays open, and Sunday 08:00 is outside study time.
	// Next week: Monday's Div. 2 is in; Wednesday 14:35 isn't study time.
	if got := plans(s); got != "ac:abc480=open cf:2261=in lc:weekly-contest-524=open cf:2271=in cf:2280=open" {
		t.Fatal(got)
	}
	// can't make Saturday: the day's freed, but nothing else this week fits
	s.round("cf:2261").Plan = "out"
	s.planRounds()
	if got := plans(s); got != "ac:abc480=open cf:2261=out lc:weekly-contest-524=open cf:2271=in cf:2280=open" {
		t.Fatal(got)
	}
}

func TestPlanSpreadsAcrossJudges(t *testing.T) {
	at(t, thu)
	writeConfig(t, "[contests]\nper_week = 3\n")
	s := fresh(t)
	sun := time.Date(2026, 10, 18, 20, 0, 0, 0, time.Local)
	s.applyCalendar("cf", []calRound{
		{ID: "cf:1", Name: "Codeforces Round 1 (Div. 2)", Start: time.Date(2026, 10, 16, 20, 5, 0, 0, time.Local), Dur: 2 * time.Hour},
		{ID: "cf:2", Name: "Codeforces Round 2 (Div. 2)", Start: sun, Dur: 2 * time.Hour},
	})
	s.applyCalendar("ac", []calRound{{ID: "ac:abc480", Name: "ABC 480", Start: sun.Add(-150 * time.Minute), Dur: 100 * time.Minute},
		{ID: "ac:abc481", Name: "ABC 481", Start: time.Date(2026, 10, 17, 17, 30, 0, 0, time.Local), Dur: 100 * time.Minute}})
	s.planRounds()
	// Fri CF, then the judge with none yet (Sat ABC), then Sunday: CF before AtCoder
	if got := plans(s); got != "cf:1=in ac:abc481=in ac:abc480=open cf:2=in" {
		t.Fatal(got)
	}
}

func TestCalendarMovesAndCancels(t *testing.T) {
	at(t, thu)
	s := fresh(t)
	withCalendar(s)
	cal := calendarWeek()["cf"]
	cal[0].Start = cal[0].Start.Add(15 * time.Minute)    // delayed
	s.applyCalendar("cf", append(cal[:2:2], cal[3:]...)) // Monday's Div. 2 is called off
	if r := s.round("cf:2261"); r.Start.Minute() != 20 || r.Gone {
		t.Fatal("a delayed round follows the calendar")
	}
	if !s.round("cf:2271").Gone {
		t.Fatal("a round dropped from the calendar is gone")
	}
	s.applyCalendar("cf", cal[:1]) // the calendar stops at Saturday: later rounds aren't vouched for
	if s.round("cf:2280").Gone {
		t.Fatal("rounds past the calendar's horizon aren't gone")
	}
}

func liveRound(s *State) *Round {
	s.Rounds = append(s.Rounds, Round{ID: "cf:2261", Name: "Codeforces Round 1128 (Div. 1 + Div. 2)", Kind: "div12",
		Start: wed.Add(-30 * time.Minute), DurSec: 7200, Plan: "in",
		Problems: []RoundProblem{{ID: "cf:2261A", Index: "A", Title: "Alpha"}, {ID: "cf:2261B", Index: "B", Title: "Beta"},
			{ID: "cf:2261C", Index: "C", Title: "Gamma"}, {ID: "cf:2261D", Index: "D", Title: "Delta"}, {ID: "cf:2261E", Index: "E", Title: "Eps"}}})
	return &s.Rounds[len(s.Rounds)-1]
}

func TestLiveRoundLocksClaudeEverywhere(t *testing.T) {
	at(t, wed) // 21:00, the round runs 20:30-22:30
	s := fresh(t)
	r := liveRound(s)
	h, _ := os.UserHomeDir()
	solved(s, "cf:1A", "solo", wed.Add(-3*time.Hour), wed.Add(-2*time.Hour)) // the day is even done
	for _, c := range []struct{ prompt, cwd string }{
		{"why WA", filepath.Join(h, "CP/codeforces/2261")},
		{"/edge-stuck", "/tmp"},
		{"fix the bug", filepath.Join(h, "Gems/kinvault")},
	} {
		if v := gateDecide(s, c.prompt, c.cwd); !v.Block || !strings.Contains(v.Reason, "No AI during a round") {
			t.Fatalf("live round must block %q in %s", c.prompt, c.cwd)
		}
	}
	if deny, _ := gateTool(s, "Read", map[string]any{"file_path": filepath.Join(h, "CP/codeforces/2261/C.cpp")}, filepath.Join(h, "CP")); !deny {
		t.Fatal("tools are off too")
	}
	if deny, _ := gateTool(s, "WebSearch", map[string]any{"query": "codeforces 2261C"}, "/tmp"); !deny {
		t.Fatal("even search")
	}
	r.Plan = "out"
	if v := gateDecide(s, "fix the bug", filepath.Join(h, "Gems/kinvault")); v.Block {
		t.Fatal("a round they can't make doesn't lock")
	}
	r.Joined = "live"
	if v := gateDecide(s, "fix the bug", "/tmp"); !v.Block {
		t.Fatal("a round they joined locks")
	}
	at(t, r.End())
	if v := gateDecide(s, "fix the bug", "/tmp"); v.Block {
		t.Fatal("the lock ends with the round")
	}
}

func TestLockCantBeSwitchedOffMidRound(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	liveRound(s)
	s.pin()
	writeConfig(t, "[contests]\nlock = false\n")
	if v := gateDecide(s, "x", "/tmp"); !v.Block {
		t.Fatal("turning the lock off applies tomorrow")
	}
	writeConfig(t, "[contests]\nenabled = false\n")
	if v := gateDecide(s, "x", "/tmp"); !v.Block {
		t.Fatal("so does turning rounds off")
	}
	at(t, wed.Add(8*time.Hour)) // 05:00, a new edge day
	s.Rounds[0].Start = now().Add(-time.Minute)
	if v := gateDecide(s, "x", "/tmp"); v.Block {
		t.Fatal("tomorrow the config holds")
	}
}

func TestLiveSubmissionsAndSettleQueueUpsolves(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	r := liveRound(s)
	st := r.Start
	subs := []roundSub{
		{Problem: "cf:2261A", At: st.Add(4 * time.Minute), Verdict: "OK", Type: "live"},
		{Problem: "cf:2261B", At: st.Add(15 * time.Minute), Verdict: "WRONG_ANSWER", Type: "live"},
		{Problem: "cf:2261B", At: st.Add(22 * time.Minute), Verdict: "OK", Type: "live"},
		{Problem: "cf:2261C", At: st.Add(25 * time.Minute), Verdict: "WRONG_ANSWER", Type: "live"},
		{Problem: "cf:2261C", At: st.Add(27 * time.Minute), Verdict: "TESTING", Pending: true, Type: "live"},
	}
	s.applyObs(roundObs{ID: r.ID, Subs: subs})
	s.applyObs(roundObs{ID: r.ID, Subs: subs}) // the same look again counts nothing twice
	if r.Joined != "live" || r.letters("solved") != "A B" || r.Problems[1].Fails != 1 || r.Problems[2].Fails != 1 || r.Problems[0].At != 240 {
		t.Fatalf("live count: joined %q solved %q %+v", r.Joined, r.letters("solved"), r.Problems)
	}
	if got, _ := s.today(); got != 2 {
		t.Fatalf("sitting it is worth 2 today, got %d", got)
	}
	subs[4].Verdict, subs[4].Pending = "OK", false
	at(t, r.End().Add(10*time.Minute))
	s.Current = &Task{ID: "cf:1900B", Kind: "new", Assigned: now(), Explain: -1} // offered, not started
	s.applyObs(roundObs{ID: r.ID, Subs: subs, Final: true})
	if r.Settled.IsZero() || r.letters("solved") != "A B C" || r.letters("upsolve") != "D E" {
		t.Fatalf("settled: %q upsolve %q", r.letters("solved"), r.letters("upsolve"))
	}
	if got, _ := s.today(); got != 3 {
		t.Fatalf("three solved: worth 3, got %d", got)
	}
	if s.Current != nil {
		t.Fatal("an unstarted new problem makes way for the upsolve")
	}
	tk, err := assign(s, nil)
	if err != nil || tk.Kind != "upsolve" || tk.ID != "cf:2261D" || tk.Policy != "ladder" || tk.RoundName != "CF 1128 D" {
		t.Fatalf("upsolve first: %+v %v", tk, err)
	}
	tk.Hints = 1
	accept(s, tk, now().Add(time.Hour))
	if r.Problems[3].State != "upsolved" || len(s.Probes) != 1 || s.Probes[0].Policy != "" {
		t.Fatalf("upsolved, re-solve scheduled outside the experiment: %+v %+v", r.Problems[3], s.Probes)
	}
	tk, _ = assign(s, nil)
	if tk == nil || tk.ID != "cf:2261E" {
		t.Fatal("then the next one")
	}
	if !strings.Contains(keys(s.filter(plan(s, window{}, 0))), "roundover:cf:2261:live") {
		t.Fatal("the round's over: say so")
	}
}

func TestMissedHeldRoundOwesUpsolves(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	// last Div. 1+2 they sat, they got two
	s.Rounds = append(s.Rounds, Round{ID: "cf:2200", Kind: "div12", Start: wed.AddDate(0, 0, -20), DurSec: 7200, Joined: "live", Settled: wed.AddDate(0, 0, -20),
		Problems: []RoundProblem{{ID: "cf:2200A", State: "solved"}, {ID: "cf:2200B", State: "solved"}, {ID: "cf:2200C", State: "tried"}}})
	r := liveRound(s)
	at(t, r.End().Add(10*time.Minute))
	s.applyObs(roundObs{ID: r.ID, Final: true})
	if r.Joined != "" || r.letters("upsolve") != "C D" {
		t.Fatalf("missed: from where they got to last time: %q", r.letters("upsolve"))
	}
	if got, _ := s.today(); got != 0 {
		t.Fatal("a missed round earns nothing")
	}
	if !strings.Contains(keys(s.filter(plan(s, window{}, 0))), "roundmissed:cf:2261") {
		t.Fatal("say it was missed")
	}
	// an open round they skipped owes nothing
	s.Rounds = append(s.Rounds, Round{ID: "cf:2300", Kind: "div2", Start: wed.Add(-3 * time.Hour), DurSec: 3600,
		Problems: []RoundProblem{{ID: "cf:2300A"}, {ID: "cf:2300B"}}})
	s.applyObs(roundObs{ID: "cf:2300", Final: true})
	if s.round("cf:2300").letters("upsolve") != "" {
		t.Fatal("open rounds owe nothing")
	}
}

func TestVirtualCountsAsSitting(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	r := liveRound(s)
	at(t, r.End().Add(10*time.Minute))
	s.applyObs(roundObs{ID: r.ID, Final: true}) // missed: owes C D by default
	at(t, wed.AddDate(0, 0, 2))
	var x cfSub
	x.Problem = cfProblem{ContestID: 2261, Index: "A"}
	x.Author.Type = "VIRTUAL"
	x.Verdict, x.Time = "OK", now().Add(-time.Hour).Unix()
	y := x
	y.Problem.Index, y.Time = "B", now().Add(-30*time.Minute).Unix()
	s.virtuals([]cfSub{y, x})
	if r.Joined != "virtual" || r.letters("solved") != "A B" || r.letters("upsolve") != "C D" {
		t.Fatalf("virtual: %q %q %q", r.Joined, r.letters("solved"), r.letters("upsolve"))
	}
	if got, _ := s.today(); got != 2 {
		t.Fatalf("a virtual counts on the day it's sat: %d", got)
	}
}

func TestUpsolvesLapse(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	r := liveRound(s)
	r.Joined = "live"
	r.Problems[0].State = "solved"
	at(t, r.End().Add(10*time.Minute))
	s.applyObs(roundObs{ID: r.ID, Final: true})
	if r.letters("upsolve") != "B C" {
		t.Fatal(r.letters("upsolve"))
	}
	at(t, wed.AddDate(0, 0, 9))
	s.tidyRounds()
	if r.letters("lapsed") != "B C" {
		t.Fatalf("a week later they lapse: %+v", r.Problems)
	}
	if _, p := s.nextUpsolve(); p != nil {
		t.Fatal("nothing left to offer")
	}
}

func TestRoundEventsBeforeAndDuring(t *testing.T) {
	at(t, thu)
	s := fresh(t)
	withCalendar(s)
	s.planRounds()
	at(t, time.Date(2026, 10, 16, 21, 0, 0, 0, time.Local)) // Friday 21:00: Saturday 20:05 is a day out
	evs := tell(s, window{}, 0)
	var eve *event
	for i := range evs {
		if evs[i].Key == "roundeve:cf:2261" {
			eve = &evs[i]
		}
	}
	if eve == nil || !strings.HasPrefix(eve.Title, "Tomorrow 20:05") || len(eve.Actions) != 2 {
		t.Fatalf("the day before: %+v", evs)
	}
	s.Rounds[1].Lesson = "" // ABC is open
	s.Rounds = append(s.Rounds, Round{ID: "cf:2000", Kind: "div2", Start: wed.AddDate(0, 0, -30), DurSec: 3600, Joined: "live", Lesson: "read constraints twice"})
	at(t, time.Date(2026, 10, 17, 19, 52, 0, 0, time.Local))
	k := keys(tell(s, window{}, 0))
	if !strings.Contains(k, "roundsoon:cf:2261") {
		t.Fatalf("15 minutes before: %s", k)
	}
	at(t, time.Date(2026, 10, 17, 20, 20, 0, 0, time.Local))
	pending(s)
	ev := tell(s, window{App: "zen", Title: "YouTube"}, 20*time.Second)
	if k := keys(ev); !strings.Contains(k, "roundlate:cf:2261") || strings.Contains(k, "offer") || strings.Contains(k, "brief") {
		t.Fatalf("in a round: only the round speaks: %s", k)
	}
	at(t, time.Date(2026, 10, 17, 20, 20, 40, 0, time.Local))
	if k := keys(tell(s, window{App: "zen", Title: "YouTube"}, 20*time.Second)); strings.Contains(k, "rounddrift") {
		t.Fatalf("a glance isn't drift: %s", k)
	}
	at(t, time.Date(2026, 10, 17, 20, 21, 10, 0, time.Local))
	if k := keys(tell(s, window{App: "zen", Title: "YouTube"}, 20*time.Second)); !strings.Contains(k, "rounddrift") {
		t.Fatalf("a minute on YouTube mid-round: %s", k)
	}
}

func TestLessonComesBack(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	r := liveRound(s)
	r.Joined = "live"
	at(t, r.End().Add(time.Hour))
	s.settle(r)
	out = &strings.Builder{}
	defer func() { out = os.Stdout }()
	if err := roundInfo(s); err != nil {
		t.Fatal(err)
	}
	if err := setLesson(s, "  read the constraints twice "); err != nil || r.Lesson != "read the constraints twice" {
		t.Fatal("lesson kept")
	}
	s.Rounds = append(s.Rounds, Round{ID: "cf:2299", Name: "Codeforces Round 1130 (Div. 2)", Kind: "div2", Plan: "in",
		Start: now().Add(10 * time.Minute), DurSec: 7200})
	for _, e := range tell(s, window{}, 0) {
		if e.Key == "roundsoon:cf:2299" && strings.Contains(e.Body, "Last lesson: read the constraints twice") {
			return
		}
	}
	t.Fatal("the lesson comes back before the next round")
}

func TestRoundStatsAndEmber(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	r := liveRound(s)
	r.Joined = "live"
	r.Problems[0].State, r.Problems[0].At = "solved", 300
	st := s.stats()
	if st.Rounds.Live == nil || st.Rounds.Live.State != "live" || st.Rounds.Live.Solved != 1 || st.Rounds.Weeks.Current != 1 {
		t.Fatalf("live in stats: %+v", st.Rounds)
	}
	b, _ := json.Marshal(st)
	if !strings.Contains(string(b), `"rounds":{`) || strings.Contains(string(b), `"upcoming":null`) {
		t.Fatal("rounds in stats, lists never null")
	}
	es := edgeStatus(s)
	if es.Round == nil || !es.Round.Locked || es.Round.Short != "CF 1128" || len(es.Round.Problems) != 5 || es.Round.Problems[0].State != "solved" {
		t.Fatalf("ember gets the round: %+v", es.Round)
	}
	if st.Today.Got != 2 || st.Calendar[len(st.Calendar)-1].Credits != 2 {
		t.Fatal("the round's credit is in the calendar")
	}
}

func TestRatingChangeLandsOnTheRound(t *testing.T) {
	at(t, wed)
	s := fresh(t)
	r := liveRound(s)
	r.Joined = "live"
	at(t, r.End().Add(2*time.Hour))
	if !s.awaitingRating("cf") {
		t.Fatal("awaiting the rating")
	}
	s.applySync(syncData{Src: "cf", Rating: 1251, History: []RatingPoint{{Rating: 1208, Round: "cf:2247"}, {Rating: 1251, Round: "cf:2261"}}})
	if !r.Rated || r.Delta != 43 || r.Rating != 1251 || s.awaitingRating("cf") {
		t.Fatalf("rated: %+v", r)
	}
	if !strings.Contains(keys(s.filter(plan(s, window{}, 0))), "rated:cf:2261") {
		t.Fatal("say the rating")
	}
}

func TestAtCoderTasks(t *testing.T) {
	tk := &Task{ID: "ac:abc480_d"}
	if p := solutionPath(tk); !strings.HasSuffix(p, "atcoder/abc480/D.cpp") {
		t.Fatal(p)
	}
	page := `<td class="text-center no-break"><a href="/contests/abc445/tasks/abc445_a">A</a></td>
							<td><a href="/contests/abc445/tasks/abc445_a">Strong &amp; Word</a></td>`
	ps := parseACTasks("abc445", page)
	if len(ps) != 1 || ps[0].ID != "ac:abc445_a" || ps[0].Index != "A" || ps[0].Title != "Strong & Word" {
		t.Fatalf("%+v", ps)
	}
	cal := `<div id="contest-table-upcoming"><table><tbody><tr>
	<td class="text-center"><a href='x'><time class='fixtime fixtime-full'>2026-10-17 21:00:00+0900</time></a></td>
	<td ><span>Ⓐ</span> <a href="/contests/abc480">AtCoder Beginner Contest 480</a></td>
	<td class="text-center">01:40</td></tr></tbody></table>`
	rs, err := parseACCalendar(cal)
	if err != nil || len(rs) != 1 || rs[0].ID != "ac:abc480" || rs[0].Dur != 100*time.Minute || rs[0].Start.Unix() != time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("%+v %v", rs, err)
	}
}

// EDGE_LIVE=1 go test -run LiveRounds reads the real calendars.
func TestLiveRounds(t *testing.T) {
	if os.Getenv("EDGE_LIVE") == "" {
		t.Skip("set EDGE_LIVE=1")
	}
	s := fresh(t)
	for _, j := range []string{"cf", "ac", "lc"} {
		cal, err := fetchCalendar(j)
		if err != nil {
			t.Fatalf("%s: %v", j, err)
		}
		t.Logf("%s: %d on the calendar", j, len(cal))
		s.applyCalendar(j, cal)
	}
	s.planRounds()
	for _, r := range s.Rounds {
		t.Logf("%s %-10s %-4s %-5s %s", r.Start.Format("Mon 02 Jan 15:04"), r.Short(), r.Judge(), map[string]string{"": "open"}[r.Plan]+r.Plan, r.Name)
	}
	// a finished round of each judge
	for _, r := range []Round{{ID: "cf:2247", Start: time.Unix(1752849300, 0), DurSec: 7200},
		{ID: "ac:abc445", Start: time.Date(2026, 2, 14, 21, 0, 0, 0, time.FixedZone("JST", 9*3600)), DurSec: 6000},
		{ID: "lc:weekly-contest-523"}} {
		ps, err := roundProblems(&r)
		if err != nil {
			t.Fatalf("%s problems: %v", r.ID, err)
		}
		t.Logf("%s: %d problems, first %+v", r.ID, len(ps), ps[0])
		if r.Judge() != "lc" {
			subs, err := roundSubs(&r)
			if err != nil {
				t.Fatalf("%s subs: %v", r.ID, err)
			}
			t.Logf("%s: %d submissions", r.ID, len(subs))
		}
	}
	_, h, err := acHistory()
	if err != nil || len(h) == 0 {
		t.Fatalf("atcoder history: %v", err)
	}
	t.Logf("atcoder last: %+v", h[len(h)-1])
}

func TestGateWaitsForTodaysRoundAndASatRoundMeetsTheDay(t *testing.T) {
	sat := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local) // weekend: 3 needed
	at(t, sat)
	s := fresh(t)
	s.Rounds = append(s.Rounds, Round{ID: "cf:2261", Name: "Codeforces Round 1128 (Div. 1 + Div. 2)", Kind: "div12", Plan: "in",
		Start: time.Date(2026, 10, 17, 20, 5, 0, 0, time.Local), DurSec: 3 * 3600,
		Problems: []RoundProblem{{ID: "cf:2261A", Index: "A"}, {ID: "cf:2261B", Index: "B"}}})
	r := &s.Rounds[0]
	if s.closed() {
		t.Fatal("the round is today's work: the gate waits for it")
	}
	at(t, r.End().Add(10*time.Minute))
	if !s.closed() {
		t.Fatal("missed it: the day's tasks are owed")
	}
	r.Joined, r.Problems[0].State = "live", "solved"
	if got, need := s.today(); got != need || s.closed() {
		t.Fatalf("one solve still meets a weekend day: %d/%d", got, need)
	}
}
