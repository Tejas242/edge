package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	gosync "sync"
	"time"
)

// Config is ~/.config/edge/config.toml. Every key is optional; the file is
// re-read when it changes. Gate rules that would make today easier only take
// effect tomorrow (see State.rules), so the file isn't a way around the gate.
type Config struct {
	CFUser, LCUser string // [accounts]

	WeekdayFrom, WeekendFrom time.Duration // [schedule] when work is offered, from midnight
	DayEnds                  time.Duration // [schedule] work before this counts for the day before

	WeekdayMin, WeekendMin, Weekly int           // [quota]
	HonestTry                      time.Duration // [quota] honest_try_min

	Gate      bool   // [gate] enabled
	Workspace string // [gate] workspace, absolute

	Sources   []string                      // [problems] cf, lc
	Rating    map[string]int                // [problems] cf_rating / lc_rating; 0 = live rating
	BandWidth int                           // [problems]
	AnyTopic  float64                       // [problems] any_topic_pct / 100
	Focus     map[string]map[string]float64 // [focus.cf], [focus.lc]

	LadderAt []time.Duration // [hints] ladder_min
	GiveUpAt time.Duration   // [hints] giveup_min

	FirstResolve, SecondResolve, RetryResolve int // [resolve] days

	Distraction *regexp.Regexp // [notify] drift_sites
	DriftAfter  time.Duration  // [notify] drift_min
	OfferEvery  time.Duration  // [notify] offer_every_min

	Terminal, Editor, Browser string // [launch]
}

func defaultConfig() *Config {
	h, _ := os.UserHomeDir()
	return &Config{
		CFUser: "screenager", LCUser: "screenager",
		WeekdayFrom: 19 * time.Hour, WeekendFrom: 10 * time.Hour, DayEnds: 4 * time.Hour,
		WeekdayMin: 1, WeekendMin: 3, Weekly: 12, HonestTry: 45 * time.Minute,
		Gate: true, Workspace: filepath.Join(h, "CP"),
		Sources: []string{"cf", "lc"}, Rating: map[string]int{}, BandWidth: 300, AnyTopic: 0.3,
		Focus: map[string]map[string]float64{
			// Weak areas from the screenager history: graphs 5, trees 2, dsu 1,
			// shortest paths 0, dp 25. Weight is how much interview mileage the tag has.
			"cf": {"graphs": 3, "trees": 3, "dfs and similar": 2, "dsu": 2, "shortest paths": 2,
				"dp": 3, "data structures": 2, "binary search": 1, "two pointers": 1, "bitmasks": 1},
			"lc": {"graph": 3, "tree": 2, "binary-tree": 2, "depth-first-search": 2, "breadth-first-search": 2,
				"union-find": 2, "shortest-path": 2, "topological-sort": 2, "dynamic-programming": 3,
				"segment-tree": 1, "monotonic-stack": 1, "sliding-window": 1, "heap-priority-queue": 1},
		},
		LadderAt:     []time.Duration{20 * time.Minute, 35 * time.Minute, 50 * time.Minute},
		GiveUpAt:     60 * time.Minute,
		FirstResolve: 5, SecondResolve: 14, RetryResolve: 3,
		Distraction: sitesRe([]string{"youtube", "reddit", "twitter", "x.com", "instagram", "netflix",
			"twitch", "hotstar", "prime video", "facebook", "9gag"}),
		DriftAfter: 2 * time.Minute, OfferEvery: 30 * time.Minute,
		Terminal: "alacritty", Editor: "nvim", Browser: "xdg-open",
	}
}

func configPath() string {
	d := os.Getenv("XDG_CONFIG_HOME")
	if d == "" {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".config")
	}
	return filepath.Join(d, "edge", "config.toml")
}

// defaultConfigText is what `edge install` writes when there's no config yet.
const defaultConfigText = `# edge: changes apply within a minute, no restart needed.
# Gate rules that make today easier (fewer tasks, gate off, another
# workspace) take effect tomorrow; stricter ones apply right away.

[accounts]
codeforces = "screenager"
leetcode   = "screenager"

[schedule]
weekday_from = "19:00"  # when problems are offered and the gate closes, Mon-Fri
weekend_from = "10:00"  # same, Sat-Sun
day_ends     = "04:00"  # work before this counts for the day before

[quota]
weekday        = 1   # tasks a weekday needs
weekend        = 3   # tasks a weekend day needs
weekly         = 12  # problems keep coming until this is met
honest_try_min = 45  # minutes of real work for a give-up to count for the day

[gate]
enabled   = true    # hold Claude Code to practice until the day's tasks are done
workspace = "~/CP"  # where practice happens; open while the gate is closed

[problems]
sources       = ["cf", "lc"]  # codeforces, leetcode
cf_rating     = 0    # 0 = your live rating; set a number to pin the band's base
lc_rating     = 0
band_width    = 300  # band = base .. base + width
any_topic_pct = 30   # share of new problems from any topic, not just the focus list

# Focus topics and their weights: higher comes up more, solved ones fade.
# Writing a [focus.*] section replaces that source's whole list.
[focus.cf]
graphs            = 3
trees             = 3
dp                = 3
"dfs and similar" = 2
dsu               = 2
"shortest paths"  = 2
"data structures" = 2
"binary search"   = 1
"two pointers"    = 1
bitmasks          = 1

[focus.lc]
graph                  = 3
dynamic-programming    = 3
tree                   = 2
binary-tree            = 2
depth-first-search     = 2
breadth-first-search   = 2
union-find             = 2
shortest-path          = 2
topological-sort       = 2
segment-tree           = 1
monotonic-stack        = 1
sliding-window         = 1
heap-priority-queue    = 1

[hints]
ladder_min = [20, 35, 50]  # minutes of work before hint 1, 2, 3 unlock (ladder style)
giveup_min = 60            # minutes of work before giving up is allowed (ladder style)

[resolve]
first_days  = 5   # first cold re-solve, the same for every problem
second_days = 14  # after passing the first
retry_days  = 3   # after failing one

[notify]
drift_sites     = ["youtube", "reddit", "twitter", "x.com", "instagram", "netflix", "twitch", "hotstar", "prime video", "facebook", "9gag"]
drift_min       = 2   # minutes on those, mid-task, before a nudge
offer_every_min = 30  # how often an unstarted problem is offered again

[launch]
terminal = "alacritty"  # must take --title, --working-directory and -e
editor   = "nvim"
browser  = "xdg-open"
`

var (
	confMu   gosync.Mutex
	conf     *Config
	confSeen time.Time // config file mtime conf was read at
	confChk  time.Time
	confErrs []string // lines that didn't parse, for status
)

// cfg is the current config, re-read when the file changes (checked at most
// every 5 seconds).
func cfg() *Config {
	confMu.Lock()
	defer confMu.Unlock()
	if conf != nil && time.Since(confChk) < 5*time.Second {
		return conf
	}
	confChk = time.Now()
	var mt time.Time
	if st, err := os.Stat(configPath()); err == nil {
		mt = st.ModTime()
	}
	if conf == nil || !mt.Equal(confSeen) {
		conf, confErrs = parseConfig(configPath())
		confSeen = mt
	}
	return conf
}

// resetConfig forgets the cached config (tests).
func resetConfig() {
	confMu.Lock()
	conf = nil
	confMu.Unlock()
}

// parseConfig reads path over the defaults; a missing file means defaults.
// Bad values keep their default and are reported, never fatal.
func parseConfig(path string) (*Config, []string) {
	c := defaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, nil
	}
	defer f.Close()
	h, _ := os.UserHomeDir()
	var bad []string
	section, fresh := "", map[string]bool{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		ln := strings.TrimSpace(stripComment(sc.Text()))
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "[") {
			section = strings.Trim(ln, "[] ")
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			bad = append(bad, fmt.Sprintf("line %d: no '='", n))
			continue
		}
		k, v = unquote(strings.TrimSpace(k)), strings.TrimSpace(v)
		fail := func() { bad = append(bad, fmt.Sprintf("line %d: bad value for %s", n, k)) }
		str := unquote(v)
		num := func(lo, hi int, set func(int)) {
			if x, err := strconv.Atoi(str); err == nil && x >= lo && x <= hi {
				set(x)
			} else {
				fail()
			}
		}
		clock := func(set func(time.Duration)) {
			hh, mm, ok := strings.Cut(str, ":")
			a, e1 := strconv.Atoi(hh)
			b, e2 := strconv.Atoi(mm)
			if !ok || e1 != nil || e2 != nil || a < 0 || a > 23 || b < 0 || b > 59 {
				fail()
				return
			}
			set(time.Duration(a)*time.Hour + time.Duration(b)*time.Minute)
		}
		if src, ok := strings.CutPrefix(section, "focus."); ok {
			w, err := strconv.ParseFloat(str, 64)
			if err != nil || w < 0 || (src != "cf" && src != "lc") {
				fail()
				continue
			}
			if !fresh[src] {
				fresh[src], c.Focus[src] = true, map[string]float64{}
			}
			c.Focus[src][k] = w
			continue
		}
		switch section + "." + k {
		case "accounts.codeforces":
			c.CFUser = str
		case "accounts.leetcode":
			c.LCUser = str
		case "schedule.weekday_from":
			clock(func(d time.Duration) { c.WeekdayFrom = d })
		case "schedule.weekend_from":
			clock(func(d time.Duration) { c.WeekendFrom = d })
		case "schedule.day_ends":
			clock(func(d time.Duration) {
				if d > 8*time.Hour {
					fail() // later than 08:00 would make the "day" start mid-morning
					return
				}
				c.DayEnds = d
			})
		case "quota.weekday":
			num(0, 10, func(x int) { c.WeekdayMin = x })
		case "quota.weekend":
			num(0, 10, func(x int) { c.WeekendMin = x })
		case "quota.weekly":
			num(0, 100, func(x int) { c.Weekly = x })
		case "quota.honest_try_min":
			num(5, 240, func(x int) { c.HonestTry = time.Duration(x) * time.Minute })
		case "gate.enabled":
			if b, err := strconv.ParseBool(str); err == nil {
				c.Gate = b
			} else {
				fail()
			}
		case "gate.workspace":
			p := str
			if strings.HasPrefix(p, "~") {
				p = h + p[1:]
			}
			if !filepath.IsAbs(p) || filepath.Clean(p) == filepath.Clean(h) || filepath.Clean(p) == "/" {
				fail() // the whole home as "practice" would switch the gate off
				continue
			}
			c.Workspace = filepath.Clean(p)
		case "problems.sources":
			var srcs []string
			for _, x := range list(v) {
				if x == "cf" || x == "lc" {
					srcs = append(srcs, x)
				}
			}
			if len(srcs) == 0 {
				fail()
				continue
			}
			c.Sources = srcs
		case "problems.cf_rating":
			num(0, 4000, func(x int) { c.Rating["cf"] = x })
		case "problems.lc_rating":
			num(0, 4000, func(x int) { c.Rating["lc"] = x })
		case "problems.band_width":
			num(100, 1000, func(x int) { c.BandWidth = x })
		case "problems.any_topic_pct":
			num(0, 100, func(x int) { c.AnyTopic = float64(x) / 100 })
		case "hints.ladder_min":
			var at []time.Duration
			for _, x := range list(v) {
				m, err := strconv.Atoi(x)
				if err != nil || m < 0 || (len(at) > 0 && time.Duration(m)*time.Minute < at[len(at)-1]) {
					at = nil
					break
				}
				at = append(at, time.Duration(m)*time.Minute)
			}
			if len(at) != 3 {
				fail()
				continue
			}
			c.LadderAt = at
		case "hints.giveup_min":
			num(0, 240, func(x int) { c.GiveUpAt = time.Duration(x) * time.Minute })
		case "resolve.first_days":
			num(1, 60, func(x int) { c.FirstResolve = x })
		case "resolve.second_days":
			num(1, 120, func(x int) { c.SecondResolve = x })
		case "resolve.retry_days":
			num(1, 60, func(x int) { c.RetryResolve = x })
		case "notify.drift_sites":
			if sites := list(v); len(sites) > 0 {
				c.Distraction = sitesRe(sites)
			} else {
				c.Distraction = nil
			}
		case "notify.drift_min":
			num(1, 60, func(x int) { c.DriftAfter = time.Duration(x) * time.Minute })
		case "notify.offer_every_min":
			num(5, 600, func(x int) { c.OfferEvery = time.Duration(x) * time.Minute })
		case "launch.terminal":
			c.Terminal = str
		case "launch.editor":
			c.Editor = str
		case "launch.browser":
			c.Browser = str
		default:
			bad = append(bad, fmt.Sprintf("line %d: unknown key %s.%s", n, section, k))
		}
	}
	return c, bad
}

// sitesRe matches any of the sites as a whole word, case-insensitively.
func sitesRe(sites []string) *regexp.Regexp {
	q := make([]string, len(sites))
	for i, s := range sites {
		q[i] = regexp.QuoteMeta(strings.TrimSpace(s))
	}
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(q, "|") + `)\b`)
}

// list parses ["a", "b"] or [1, 2] into its items.
func list(v string) []string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil
	}
	var out []string
	for _, x := range strings.Split(v[1:len(v)-1], ",") {
		if x = unquote(strings.TrimSpace(x)); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// stripComment cuts a # comment that isn't inside quotes.
func stripComment(ln string) string {
	quote := rune(0)
	for i, c := range ln {
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == 0 && c == '#':
			return ln[:i]
		}
	}
	return ln
}

// Rules are the gate's terms for one day. The first time a day is seen its
// rules are pinned in the state; a config edit can then only make that day
// stricter. Looser rules start the next day.
type Rules struct {
	Day        time.Time `json:"day"`
	WeekdayMin int       `json:"weekday_min"`
	WeekendMin int       `json:"weekend_min"`
	Gate       bool      `json:"gate"`
	Workspace  string    `json:"workspace"`
}

func (c *Config) rules(d time.Time) Rules {
	return Rules{Day: d, WeekdayMin: c.WeekdayMin, WeekendMin: c.WeekendMin, Gate: c.Gate, Workspace: c.Workspace}
}

// rules are today's effective gate terms: the config, held to what was pinned today.
func (s *State) rules() Rules {
	r := cfg().rules(day(now()))
	p := s.Pinned
	if p == nil || !p.Day.Equal(r.Day) {
		return r
	}
	r.WeekdayMin, r.WeekendMin = max(r.WeekdayMin, p.WeekdayMin), max(r.WeekendMin, p.WeekendMin)
	r.Gate = r.Gate || p.Gate
	if p.Workspace != r.Workspace {
		r.Workspace = p.Workspace // a different practice dir starts tomorrow
	}
	return r
}

// pin records today's rules the first time today is seen (any writer does it).
func (s *State) pin() {
	if r := s.rules(); s.Pinned == nil || !s.Pinned.Day.Equal(r.Day) {
		s.Pinned = &r
	}
}
