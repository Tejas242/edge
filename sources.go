package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	gosync "sync"
	"time"
)

type Problem struct {
	ID      string
	Title   string
	URL     string
	Rating  int
	Tags    []string
	Contest int
}

// A hanging connection must not stall a tick for long.
var client = &http.Client{Timeout: 15 * time.Second}

func fetch(req *http.Request) ([]byte, error) {
	req.Header.Set("User-Agent", "edge/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", req.URL.Host, resp.StatusCode)
	}
	return b, nil
}

func get(url string) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	return fetch(req)
}

func cacheDir() string {
	if d := os.Getenv("EDGE_CACHE"); d != "" {
		return d
	}
	d, _ := os.UserCacheDir()
	return filepath.Join(d, "edge")
}

// cached returns the file's bytes if younger than ttl, else refetches. A
// fetch that fails or doesn't validate keeps the old copy: stale beats nothing.
func cached(name string, ttl time.Duration, f func() ([]byte, error), valid func([]byte) error) ([]byte, error) {
	p := filepath.Join(cacheDir(), name)
	if st, err := os.Stat(p); err == nil && now().Sub(st.ModTime()) < ttl {
		if b, err := os.ReadFile(p); err == nil && valid(b) == nil {
			return b, nil
		}
	}
	b, err := f()
	if err == nil {
		err = valid(b)
	}
	if err != nil {
		if old, rerr := os.ReadFile(p); rerr == nil && valid(old) == nil {
			return old, nil
		}
		return nil, err
	}
	if os.MkdirAll(cacheDir(), 0o755) == nil {
		writeAtomic(p, b, 0o644) // a cache that can't be written is still a good fetch
	}
	return b, nil
}

// ---- Codeforces ----

type cfProblem struct {
	ContestID int      `json:"contestId"`
	Index     string   `json:"index"`
	Name      string   `json:"name"`
	Rating    int      `json:"rating"`
	Tags      []string `json:"tags"`
}

func (p cfProblem) key() string { return fmt.Sprintf("cf:%d%s", p.ContestID, p.Index) }

func cfURL(contest int, index string) string {
	return fmt.Sprintf("https://codeforces.com/problemset/problem/%d/%s", contest, index)
}

var (
	cfMu   gosync.Mutex
	cfLast time.Time
)

// cfAPI calls the Codeforces API, at most once per 2 seconds (their limit).
func cfAPI(method string, out any) error {
	cfMu.Lock()
	if wait := 2100*time.Millisecond - time.Since(cfLast); wait > 0 {
		time.Sleep(wait)
	}
	b, err := get("https://codeforces.com/api/" + method)
	cfLast = time.Now()
	cfMu.Unlock()
	if err != nil {
		return err
	}
	return cfDecode(b, out)
}

func cfDecode(b []byte, out any) error {
	var env struct {
		Status  string          `json:"status"`
		Comment string          `json:"comment"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	if env.Status != "OK" {
		return fmt.Errorf("codeforces: %s", env.Comment)
	}
	return json.Unmarshal(env.Result, out)
}

type cfSub struct {
	Problem cfProblem `json:"problem"`
	Author  struct {
		Type string `json:"participantType"` // CONTESTANT | OUT_OF_COMPETITION | VIRTUAL | PRACTICE
	} `json:"author"`
	Verdict string `json:"verdict"`
	Passed  int    `json:"passedTestCount"`
	Time    int64  `json:"creationTimeSeconds"`
}

func cfSubs(count int) ([]cfSub, error) {
	if err := needHandle("Codeforces", cfg().CFUser); err != nil {
		return nil, err
	}

	var subs []cfSub
	q := "user.status?handle=" + cfg().CFUser
	if count > 0 {
		q += "&from=1&count=" + strconv.Itoa(count)
	}
	return subs, cfAPI(q, &subs)
}

func cfRating() (int, error) {
	if err := needHandle("Codeforces", cfg().CFUser); err != nil {
		return 0, err
	}

	var u []struct {
		Rating int `json:"rating"`
	}
	if err := cfAPI("user.info?handles="+cfg().CFUser, &u); err != nil || len(u) == 0 {
		return 0, err
	}
	return u[0].Rating, nil
}

func cfProblems() ([]Problem, error) {
	b, err := cached("cf_problems.json", 24*time.Hour, func() ([]byte, error) {
		var raw json.RawMessage
		if err := cfAPI("problemset.problems", &raw); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"status": "OK", "result": raw})
	}, func(b []byte) error {
		var r struct {
			Problems []cfProblem `json:"problems"`
		}
		if err := cfDecode(b, &r); err != nil {
			return err
		}
		if len(r.Problems) < 1000 {
			return fmt.Errorf("codeforces: only %d problems in the list", len(r.Problems))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parseCF(b)
}

// parseCF turns cf_problems.json's bytes into problems.
func parseCF(b []byte) ([]Problem, error) {
	var r struct {
		Problems []cfProblem `json:"problems"`
	}
	if err := cfDecode(b, &r); err != nil {
		return nil, err
	}
	out := make([]Problem, 0, len(r.Problems))
	for _, p := range r.Problems {
		out = append(out, Problem{ID: p.key(), Title: p.Name, URL: cfURL(p.ContestID, p.Index),
			Rating: p.Rating, Tags: p.Tags, Contest: p.ContestID})
	}
	return out, nil
}

// ---- LeetCode ----

func lcQuery(query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, _ := http.NewRequest("POST", "https://leetcode.com/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", "https://leetcode.com")
	b, err := fetch(req)
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("leetcode: %s", env.Errors[0].Message)
	}
	return json.Unmarshal(env.Data, out)
}

type lcAC struct {
	Slug string `json:"titleSlug"`
	Time string `json:"timestamp"`
}

// lcProfile is the contest rating, its history and the last 20 accepts.
func lcProfile() (rating int, history []RatingPoint, recent []lcAC, err error) {
	if err := needHandle("LeetCode", cfg().LCUser); err != nil {
		return 0, nil, nil, err
	}

	var r struct {
		Rank *struct {
			Rating float64 `json:"rating"`
		} `json:"userContestRanking"`
		History []struct {
			Attended bool    `json:"attended"`
			Rating   float64 `json:"rating"`
			Contest  struct {
				Title     string `json:"title"`
				Slug      string `json:"titleSlug"`
				StartTime int64  `json:"startTime"`
			} `json:"contest"`
		} `json:"userContestRankingHistory"`
		Recent []lcAC `json:"recentAcSubmissionList"`
	}
	err = lcQuery(`query($u:String!){userContestRanking(username:$u){rating}
		userContestRankingHistory(username:$u){attended rating contest{title titleSlug startTime}}
		recentAcSubmissionList(username:$u,limit:20){titleSlug timestamp}}`, map[string]any{"u": cfg().LCUser}, &r)
	if r.Rank != nil {
		rating = int(r.Rank.Rating)
	}
	for _, h := range r.History {
		if h.Attended {
			history = append(history, RatingPoint{At: time.Unix(h.Contest.StartTime, 0), Rating: int(h.Rating), Contest: h.Contest.Title, Round: "lc:" + h.Contest.Slug})
		}
	}
	return rating, history, r.Recent, err
}

// cfRatingHistory is every rated contest's result.
func cfRatingHistory() ([]RatingPoint, error) {
	if err := needHandle("Codeforces", cfg().CFUser); err != nil {
		return nil, err
	}

	var rs []struct {
		ID      int    `json:"contestId"`
		Contest string `json:"contestName"`
		Rating  int    `json:"newRating"`
		At      int64  `json:"ratingUpdateTimeSeconds"`
	}
	if err := cfAPI("user.rating?handle="+cfg().CFUser, &rs); err != nil {
		return nil, err
	}
	out := make([]RatingPoint, 0, len(rs))
	for _, r := range rs {
		out = append(out, RatingPoint{At: time.Unix(r.At, 0), Rating: r.Rating, Contest: r.Contest, Round: fmt.Sprintf("cf:%d", r.ID)})
	}
	return out, nil
}

// lcSub is one of the user's recent LeetCode submissions, any verdict.
type lcSub struct {
	Slug   string `json:"titleSlug"`
	Time   string `json:"timestamp"`
	Status string `json:"statusDisplay"` // "Accepted", "Wrong Answer", ...
}

func (x lcSub) unix() int64 {
	ts, _ := strconv.ParseInt(x.Time, 10, 64)
	return ts
}

// lcSubs is the public recent-submissions list (last 20, all verdicts).
func lcSubs() ([]lcSub, error) {
	if err := needHandle("LeetCode", cfg().LCUser); err != nil {
		return nil, err
	}

	var r struct {
		Subs []lcSub `json:"recentSubmissionList"`
	}
	err := lcQuery(`query($u:String!){recentSubmissionList(username:$u,limit:20){titleSlug timestamp statusDisplay}}`,
		map[string]any{"u": cfg().LCUser}, &r)
	return r.Subs, err
}

type lcQ struct {
	ID    string `json:"questionFrontendId"`
	Title string `json:"title"`
	Slug  string `json:"titleSlug"`
	Paid  bool   `json:"isPaidOnly"`
	Tags  []struct {
		Slug string `json:"slug"`
	} `json:"topicTags"`
}

func lcQuestions() (map[string]lcQ, error) {
	b, err := cached("lc_questions.json", 7*24*time.Hour, func() ([]byte, error) {
		var all []lcQ
		for skip := 0; ; skip += 100 {
			var r struct {
				Q struct {
					Total int   `json:"totalNum"`
					Data  []lcQ `json:"data"`
				} `json:"questionList"`
			}
			err := lcQuery(`query($s:Int,$l:Int){questionList(categorySlug:"",limit:$l,skip:$s,filters:{}){
				totalNum data{questionFrontendId title titleSlug isPaidOnly topicTags{slug}}}}`,
				map[string]any{"s": skip, "l": 100}, &r)
			if err != nil {
				return nil, err
			}
			all = append(all, r.Q.Data...)
			if len(r.Q.Data) < 100 || len(all) >= r.Q.Total {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		return json.Marshal(all)
	}, func(b []byte) error {
		var all []lcQ
		if err := json.Unmarshal(b, &all); err != nil {
			return err
		}
		if len(all) < 1000 {
			return fmt.Errorf("leetcode: only %d questions in the list", len(all))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parseLCQuestions(b)
}

// parseLCQuestions is lc_questions.json by slug.
func parseLCQuestions(b []byte) (map[string]lcQ, error) {
	var all []lcQ
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	m := make(map[string]lcQ, len(all))
	for _, q := range all {
		m[q.Slug] = q
	}
	return m, nil
}

// lcProblems joins zerotrac's contest-problem ratings with LeetCode's tags.
func lcProblems() ([]Problem, error) {
	qs, err := lcQuestions()
	if err != nil {
		return nil, err
	}
	b, err := cached("lc_ratings.txt", 7*24*time.Hour, func() ([]byte, error) {
		return get("https://raw.githubusercontent.com/zerotrac/leetcode_problem_rating/main/ratings.txt")
	}, func(b []byte) error {
		if n := bytes.Count(b, []byte("\n")); n < 1000 || !bytes.Contains(b[:min(len(b), 200)], []byte("\t")) {
			return fmt.Errorf("zerotrac ratings: unexpected file (%d lines)", n)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parseLC(qs, b), nil
}

// parseLC joins zerotrac's ratings.txt with the question list: rated, free
// problems only.
func parseLC(qs map[string]lcQ, b []byte) []Problem {
	var out []Problem
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 5 {
			continue
		}
		r, _ := strconv.ParseFloat(f[0], 64)
		q, ok := qs[f[4]]
		if !ok || q.Paid {
			continue
		}
		p := Problem{ID: "lc:" + q.Slug, Title: q.ID + ". " + q.Title,
			URL: "https://leetcode.com/problems/" + q.Slug + "/", Rating: int(r)}
		for _, t := range q.Tags {
			p.Tags = append(p.Tags, t.Slug)
		}
		out = append(out, p)
	}
	return out
}

var (
	tagRe   = regexp.MustCompile(`<[^>]+>`)
	blankRe = regexp.MustCompile(`\n{3,}`)
)

func lcStatement(slug string) (string, error) {
	var r struct {
		Q struct {
			Content string `json:"content"`
		} `json:"question"`
	}
	err := lcQuery(`query($s:String!){question(titleSlug:$s){content}}`, map[string]any{"s": slug}, &r)
	txt := html.UnescapeString(tagRe.ReplaceAllString(r.Q.Content, ""))
	return strings.TrimSpace(blankRe.ReplaceAllString(txt, "\n\n")), err
}

// needHandle stops a judge call before it asks about nobody: the handles are
// the user's own, set under [accounts] in the config.
func needHandle(site, handle string) error {
	if handle == "" {
		return fmt.Errorf("no %s handle: set it under [accounts] in ~/.config/edge/config.toml", site)
	}
	return nil
}
