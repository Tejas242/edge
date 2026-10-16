package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The network half of rounds: contest calendars, a round's problems, the
// user's submissions in it, AtCoder as a judge. Call these outside the lock.

// calRound is one contest as a judge's calendar lists it.
type calRound struct {
	ID    string // cf:2261 | ac:abc480 | lc:weekly-contest-524
	Name  string
	Start time.Time
	Dur   time.Duration
}

func fetchCalendar(judge string) ([]calRound, error) {
	switch judge {
	case "cf":
		return cfCalendar()
	case "ac":
		return acCalendar()
	case "lc":
		return lcCalendar()
	}
	return nil, fmt.Errorf("unknown judge %q", judge)
}

func cfCalendar() ([]calRound, error) {
	var cs []struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Phase string `json:"phase"`
		Dur   int64  `json:"durationSeconds"`
		Start int64  `json:"startTimeSeconds"`
	}
	if err := cfAPI("contest.list?gym=false", &cs); err != nil {
		return nil, err
	}
	var out []calRound
	for _, c := range cs {
		if c.Phase == "BEFORE" || c.Phase == "CODING" {
			out = append(out, calRound{ID: fmt.Sprintf("cf:%d", c.ID), Name: c.Name, Start: time.Unix(c.Start, 0), Dur: time.Duration(c.Dur) * time.Second})
		}
	}
	return out, nil
}

var (
	acTableRe = regexp.MustCompile(`(?s)id="contest-table-(action|upcoming)">(.*?)</table>`)
	acRowRe   = regexp.MustCompile(`(?s)<time class='fixtime fixtime-full'>([^<]+)</time>.*?href="/contests/([a-z0-9_-]+)">([^<]+)</a>.*?<td class="text-center">(\d+):(\d+)</td>`)
)

// acCalendar reads the running and upcoming tables of atcoder.jp/contests.
func acCalendar() ([]calRound, error) {
	b, err := get("https://atcoder.jp/contests/")
	if err != nil {
		return nil, err
	}
	return parseACCalendar(string(b))
}

func parseACCalendar(page string) ([]calRound, error) {
	var out []calRound
	tables := acTableRe.FindAllStringSubmatch(page, -1)
	if len(tables) == 0 {
		return nil, fmt.Errorf("atcoder: no contest tables on the page")
	}
	for _, t := range tables {
		for _, m := range acRowRe.FindAllStringSubmatch(t[2], -1) {
			start, err := time.Parse("2006-01-02 15:04:05-0700", m[1])
			if err != nil {
				continue
			}
			h, _ := strconv.Atoi(m[4])
			mm, _ := strconv.Atoi(m[5])
			out = append(out, calRound{ID: "ac:" + m[2], Name: strings.TrimSpace(htmlText(m[3])), Start: start.Local(),
				Dur: time.Duration(h)*time.Hour + time.Duration(mm)*time.Minute})
		}
	}
	return out, nil
}

func lcCalendar() ([]calRound, error) {
	var r struct {
		Up []struct {
			Title string `json:"title"`
			Slug  string `json:"titleSlug"`
			Start int64  `json:"startTime"`
			Dur   int64  `json:"duration"`
		} `json:"upcomingContests"`
	}
	if err := lcQuery(`{upcomingContests{title titleSlug startTime duration}}`, nil, &r); err != nil {
		return nil, err
	}
	var out []calRound
	for _, c := range r.Up {
		out = append(out, calRound{ID: "lc:" + c.Slug, Name: c.Title, Start: time.Unix(c.Start, 0), Dur: time.Duration(c.Dur) * time.Second})
	}
	return out, nil
}

// ---- a round's problems ----

// roundProblems lists a round's problems in contest order. Codeforces only
// shows them once it has started.
func roundProblems(r *Round) ([]RoundProblem, error) {
	switch r.Judge() {
	case "cf":
		var st struct {
			Problems []cfProblem `json:"problems"`
		}
		// the only form Codeforces allows anonymously: the whole table
		// (gzipped, a few hundred KB), so it's fetched once per round
		if err := cfAPI("contest.standings?contestId="+r.Key(), &st); err != nil {
			return nil, err
		}
		var out []RoundProblem
		for _, p := range st.Problems {
			out = append(out, RoundProblem{ID: p.key(), Index: p.Index, Title: p.Name,
				URL: fmt.Sprintf("https://codeforces.com/contest/%d/problem/%s", p.ContestID, p.Index), Rating: p.Rating})
		}
		return out, nil
	case "ac":
		b, err := get("https://atcoder.jp/contests/" + r.Key() + "/tasks")
		if err != nil {
			return nil, err
		}
		out := parseACTasks(r.Key(), string(b))
		if len(out) == 0 {
			return nil, fmt.Errorf("atcoder: no tasks listed for %s yet", r.Key())
		}
		if d, err := acDifficulties(); err == nil {
			for i := range out {
				out[i].Rating = d[strings.TrimPrefix(out[i].ID, "ac:")]
			}
		}
		return out, nil
	case "lc":
		var q struct {
			List []struct {
				Title string `json:"title"`
				Slug  string `json:"titleSlug"`
			} `json:"contestQuestionList"`
		}
		if err := lcQuery(`query($s:String!){contestQuestionList(contestSlug:$s){title titleSlug}}`, map[string]any{"s": r.Key()}, &q); err != nil {
			return nil, err
		}
		var out []RoundProblem
		for i, p := range q.List {
			out = append(out, RoundProblem{ID: "lc:" + p.Slug, Index: fmt.Sprintf("Q%d", i+1), Title: p.Title,
				URL: "https://leetcode.com/problems/" + p.Slug + "/"})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("leetcode: no problems listed for %s yet", r.Key())
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown judge")
}

var acTaskRe = regexp.MustCompile(`<td class="text-center no-break"><a href="/contests/[^/]+/tasks/([a-z0-9_]+)">([^<]+)</a></td>\s*<td><a href="/contests/[^/]+/tasks/[a-z0-9_]+">([^<]+)</a>`)

func parseACTasks(contest, page string) []RoundProblem {
	var out []RoundProblem
	for _, m := range acTaskRe.FindAllStringSubmatch(page, -1) {
		out = append(out, RoundProblem{ID: "ac:" + m[1], Index: strings.TrimSpace(m[2]), Title: strings.TrimSpace(htmlText(m[3])),
			URL: "https://atcoder.jp/contests/" + contest + "/tasks/" + m[1]})
	}
	return out
}

// acDifficulties is kenkoooo's estimated difficulty per AtCoder task, on
// AtCoder's rating scale (it appears a day or so after a contest).
func acDifficulties() (map[string]int, error) {
	b, err := cached("ac_models.json", 24*time.Hour, func() ([]byte, error) {
		return get("https://kenkoooo.com/atcoder/resources/problem-models.json")
	}, func(b []byte) error {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if len(m) < 1000 {
			return fmt.Errorf("kenkoooo: only %d problem models", len(m))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var m map[string]struct {
		D *float64 `json:"difficulty"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		if v.D != nil {
			// below 400, kenkoooo shows difficulties squashed the way AtCoder shows ratings
			d := *v.D
			if d < 400 {
				d = 400 / math.Exp(1-d/400)
			}
			out[k] = int(d)
		}
	}
	return out, nil
}

// ---- submissions ----

// roundSub is one submission to a round's problem.
type roundSub struct {
	Problem string // task id
	At      time.Time
	Verdict string // OK, or the judge's word for what went wrong
	Pending bool   // still being judged
	Type    string // live | virtual | practice
}

func (x roundSub) ok() bool { return x.Verdict == "OK" }

// roundSubs is what the user submitted to the round's problems.
func roundSubs(r *Round) ([]roundSub, error) {
	var out []roundSub
	switch r.Judge() {
	case "cf":
		subs, err := cfSubs(100)
		if err != nil {
			return nil, err
		}
		for _, x := range subs {
			if strconv.Itoa(x.Problem.ContestID) != r.Key() {
				continue
			}
			typ := "practice"
			switch x.Author.Type {
			case "CONTESTANT", "OUT_OF_COMPETITION":
				typ = "live"
			case "VIRTUAL":
				typ = "virtual"
			}
			out = append(out, roundSub{Problem: x.Problem.key(), At: time.Unix(x.Time, 0), Verdict: x.Verdict,
				Pending: x.Verdict == "" || x.Verdict == "TESTING", Type: typ})
		}
	case "ac":
		subs, err := acSubs(r.Start.Add(-time.Minute))
		if err != nil {
			return nil, err
		}
		for _, x := range subs {
			if x.Contest != r.Key() {
				continue
			}
			out = append(out, roundSub{Problem: "ac:" + x.Problem, At: time.Unix(x.Time, 0), Verdict: acVerdict(x.Result),
				Pending: x.Result == "WJ" || x.Result == "WR" || strings.HasSuffix(x.Result, "/"), Type: inWindow(r, x.Time)})
		}
	case "lc":
		subs, err := lcSubs()
		if err != nil {
			return nil, err
		}
		in := map[string]bool{}
		for _, p := range r.Problems {
			in[p.ID] = true
		}
		for _, x := range subs {
			if !in["lc:"+x.Slug] {
				continue
			}
			v := x.Status
			if v == "Accepted" {
				v = "OK"
			}
			out = append(out, roundSub{Problem: "lc:" + x.Slug, At: time.Unix(x.unix(), 0), Verdict: v, Type: inWindow(r, x.unix())})
		}
	}
	slices.SortFunc(out, func(a, b roundSub) int { return a.At.Compare(b.At) })
	return out, nil
}

// inWindow: AtCoder and LeetCode don't say how a submission was made, so
// one sent while the round ran counts as sitting it.
func inWindow(r *Round, unix int64) string {
	if t := time.Unix(unix, 0); !t.Before(r.Start) && t.Before(r.End()) {
		return "live"
	}
	return "practice"
}

// acSub is one AtCoder submission, from kenkoooo's mirror (atcoder.jp's own
// lists need a login). AtCoder hides submissions while a contest runs, so
// they appear a few minutes after it ends.
type acSub struct {
	Problem string `json:"problem_id"`
	Contest string `json:"contest_id"`
	Time    int64  `json:"epoch_second"`
	Result  string `json:"result"`
}

func acSubs(from time.Time) ([]acSub, error) {
	b, err := get("https://kenkoooo.com/atcoder/atcoder-api/v3/user/submissions?user=" + url.QueryEscape(cfg().ACUser) +
		"&from_second=" + strconv.FormatInt(from.Unix(), 10))
	if err != nil {
		return nil, err
	}
	var out []acSub
	return out, json.Unmarshal(b, &out)
}

func acVerdict(r string) string {
	if r == "AC" {
		return "OK"
	}
	return r
}

// acHistory is the user's rated AtCoder contests.
func acHistory() (rating int, hist []RatingPoint, err error) {
	b, err := get("https://atcoder.jp/users/" + url.PathEscape(cfg().ACUser) + "/history/json")
	if err != nil {
		return 0, nil, err
	}
	var hs []struct {
		Rated   bool   `json:"IsRated"`
		New     int    `json:"NewRating"`
		Contest string `json:"ContestScreenName"`
		Name    string `json:"ContestName"`
		End     string `json:"EndTime"`
	}
	if err := json.Unmarshal(b, &hs); err != nil {
		return 0, nil, err
	}
	for _, h := range hs {
		if !h.Rated {
			continue
		}
		at, _ := time.Parse(time.RFC3339, h.End)
		hist = append(hist, RatingPoint{At: at.Local(), Rating: h.New, Contest: h.Name, Round: "ac:" + strings.TrimSuffix(h.Contest, ".contest.atcoder.jp")})
		rating = h.New
	}
	return rating, hist, nil
}

var entityRe = regexp.MustCompile(`&(amp|lt|gt|quot|#39);`)

func htmlText(s string) string {
	return entityRe.ReplaceAllStringFunc(s, func(e string) string {
		return map[string]string{"&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": `"`, "&#39;": "'"}[e]
	})
}
