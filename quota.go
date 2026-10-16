package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// day is the start of the edge day t falls in. Days end at [schedule]
// day_ends (04:00), so a problem finished at 00:30 counts for the evening it
// was worked on.
func day(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Add(-cfg().DayEnds).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func weekStart(t time.Time) time.Time {
	d := day(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // Monday
}

func weekend(d time.Time) bool {
	wd := day(d).Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// studyTime: from [schedule] weekday_from / weekend_from until the day ends.
func studyTime(t time.Time) bool {
	from := cfg().WeekdayFrom
	if weekend(t) {
		from = cfg().WeekendFrom
	}
	return t.Sub(day(t)) >= from
}

// dailyMin is the tasks needed on the day instant t falls in.
func (s *State) dailyMin(t time.Time) int {
	r := s.rules()
	if weekend(t) {
		return r.WeekendMin
	}
	return r.WeekdayMin
}

// credit: an accepted solution counts; so does a give-up after enough real
// work (focused time, not wall-clock) that left a trace: a submission or an
// edited solution file.
func credit(t Task) int {
	switch t.Outcome {
	case "solo", "assisted":
		return 1
	case "gaveup":
		if t.Worked() >= cfg().HonestTry && (t.Fails > 0 || t.Edited) {
			return 1
		}
	}
	return 0
}

func (s *State) creditsSince(from time.Time) int {
	n := 0
	for _, t := range s.History {
		if !t.Finished.Before(from) {
			n += credit(t)
		}
	}
	for i := range s.Rounds {
		if r := &s.Rounds[i]; r.Joined != "" && !r.creditAt().Before(from) {
			n += s.roundCredit(r)
		}
	}
	return n
}

func (s *State) today() (got, need int) {
	return s.creditsSince(day(now())), s.dailyMin(now())
}

// streak counts consecutive days, ending today or yesterday, that met the
// minimum. A pass day neither counts nor breaks it.
func (s *State) streak() int {
	byDay := map[time.Time]int{}
	for _, t := range s.History {
		if c := credit(t); c > 0 {
			byDay[day(t.Finished)] += c
		}
	}
	for i := range s.Rounds {
		if r := &s.Rounds[i]; r.Joined != "" {
			byDay[day(r.creditAt())] += s.roundCredit(r)
		}
	}
	// d is a day's start; d+12h is safely inside it (day() of the start
	// itself would fall before day_ends, in the day before)
	d := day(now())
	if byDay[d] < s.dailyMin(d.Add(12*time.Hour)) {
		d = d.AddDate(0, 0, -1)
	}
	n := 0
	for i := 0; i < 3650; i++ {
		switch mid := d.Add(12 * time.Hour); {
		case byDay[d] >= s.dailyMin(mid):
			n++
		case !s.passOn(mid):
			return n
		}
		d = d.AddDate(0, 0, -1)
	}
	return n
}

func (s *State) passOn(d time.Time) bool {
	for _, p := range s.Passes {
		if day(p).Equal(day(d)) {
			return true
		}
	}
	return false
}

func (s *State) passUsedThisWeek() bool {
	ws := weekStart(now())
	for _, p := range s.Passes {
		if !p.Before(ws) {
			return true
		}
	}
	return false
}

// workDir is the practice workspace, ~/CP by default ([gate] workspace).
func workDir() string {
	if d := os.Getenv("EDGE_WORK"); d != "" {
		return d // tests: never touch the real ~/CP
	}
	return cfg().Workspace
}

// practiceDir is the workspace the gate holds to today (a changed
// workspace starts tomorrow).
func (s *State) practiceDir() string {
	if os.Getenv("EDGE_WORK") != "" {
		return workDir()
	}
	return s.rules().Workspace
}

type verdict struct {
	Block   bool
	Reason  string
	Context string // injected into Claude's context when allowed
}

// closed: it's study time, today's minimum isn't met, and nothing excuses
// it. Outside study time the gate is open: no work is on offer, so there is
// nothing to hold out for.
func (s *State) closed() bool {
	if !s.rules().Gate || s.GateFrom.IsZero() || now().Before(s.GateFrom) || !studyTime(now()) {
		return false
	}
	got, need := s.today()
	return got < need && !s.passOn(now()) && s.failOpen() == "" && s.roundToday() == nil
}

// failOpen says why the gate must open even though the work isn't done:
// edge itself can't give the user a way to earn the day.
func (s *State) failOpen() string {
	if t, ok := s.Trouble["assign"]; ok {
		return t.Msg
	}
	if c := s.Current; c != nil {
		if t, ok := s.Trouble[c.Source()]; ok && now().Sub(t.Since) >= troubleAfter {
			return t.Msg
		}
	}
	return ""
}

// under reports whether p is dir or inside it, after resolving symlinks.
func under(p, dir string) bool {
	p, dir = realPath(p), realPath(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// realPath resolves symlinks in the longest existing prefix of p.
func realPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for q := p; ; q = filepath.Dir(q) {
		if r, err := filepath.EvalSymlinks(q); err == nil {
			return filepath.Join(r, rest)
		}
		if q == filepath.Dir(q) {
			return p
		}
		rest = filepath.Join(filepath.Base(q), rest)
	}
}

// gateDecide is the prompt half of the lockout. While closed, only practice
// gets through: prompts from the workspace or edge's own skills, with a
// contract that keeps Claude on practice. gateTool enforces the same thing
// on tool calls.
func gateDecide(s *State, prompt, cwd string) verdict {
	if r := s.roundLock(); r != nil {
		return verdict{Block: true, Reason: lockReason(r)} // no exceptions: not the workspace, not edge's skills
	}
	p := strings.TrimSpace(prompt)
	ws := s.practiceDir()
	inWS := cwd != "" && under(cwd, ws)
	if !s.closed() {
		if inWS {
			return verdict{Context: tutorContract(s)}
		}
		return verdict{}
	}
	if inWS || strings.HasPrefix(p, "/edge") {
		return verdict{Context: tutorContract(s) + " " + lockedContract}
	}
	got, need := s.today()
	var b strings.Builder
	fmt.Fprintf(&b, "edge gate: today's learning isn't done (%d/%d).", got, need)
	if t := s.Current; t != nil && t.Active() {
		fmt.Fprintf(&b, "\nYou're on %s (%s), %s of work in.", t.Title, t.URL, dur(t.Worked()))
	} else if t != nil {
		fmt.Fprintf(&b, "\nWaiting for you: %s (%s). Click Start on the notification.", t.Title, t.URL)
	}
	fmt.Fprintf(&b, "\nTalk to Claude from %s about it any time.", tilde(ws))
	if !s.passUsedThisWeek() {
		b.WriteString("\nReal emergency? Run `edge pass` in a terminal: it unlocks today (1 per week).")
	}
	return verdict{Block: true, Reason: b.String()}
}

func tilde(p string) string {
	if h, _ := os.UserHomeDir(); h != "" && strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}

const lockedContract = "GATE CLOSED: today's practice isn't done. Only help with competitive programming / DSA practice. " +
	"Decline anything else (other projects, deploys, config) with one line: it unlocks after today's problem. " +
	"Never offer to disable, edit or work around the edge gate or its hooks."

var (
	shellSplit = regexp.MustCompile(`[\s'"=;&|<>()` + "`" + `]+`)
	cdRe       = regexp.MustCompile(`(?:^|[;&|(\n]|&&|\|\|)\s*(?:cd|pushd|popd)\b\s*([^;&|)\n]*)`)
	// Commands that run other code or stop edge: a path check can't see
	// what they'll touch. Compiling and running their own C++ is the job.
	shellWords = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
		"python": true, "python2": true, "python3": true, "perl": true, "ruby": true, "node": true, "deno": true,
		"bun": true, "lua": true, "php": true, "eval": true, "source": true, "exec": true, "xargs": true,
		"systemctl": true, "kill": true, "pkill": true, "killall": true, "crontab": true, "nohup": true,
		"setsid": true, "sudo": true, "doas": true, "su": true, "env": true, "busybox": true,
		"script": true, "unbuffer": true, "expect": true, "socat": true, "tmux": true, "screen": true}
	// Tools that touch nothing outside the session, or are checked below.
	safeTools = []string{"WebFetch", "WebSearch", "Skill", "Task", "Agent", "TodoWrite", "ToolSearch",
		"AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "BashOutput", "KillShell", "TaskOutput", "TaskStop"}
)

// gateTool is the hard half: while closed, tools may only touch the
// workspace. It's a speed bump, not a sandbox (a compiled program can read
// anything); the prompt contract and honest credit do the real work.
func gateTool(s *State, tool string, input map[string]any, cwd string) (deny bool, reason string) {
	if r := s.roundLock(); r != nil {
		return true, lockReason(r) + " Stop here and tell the user; don't retry."
	}
	if !s.closed() {
		return false, ""
	}
	h, _ := os.UserHomeDir()
	ws := s.practiceDir()
	no := func(what string) (bool, string) {
		return true, "edge gate: " + what + " is locked until today's practice is done. " +
			"Only " + tilde(ws) + " is open. Tell the user it unlocks after today's problem; never offer to disable or edit the gate."
	}
	resolve := func(p string) string {
		p = strings.NewReplacer("${HOME}", h, "$HOME", h).Replace(p)
		if strings.HasPrefix(p, "~") {
			p = h + p[1:]
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		return filepath.Clean(p)
	}
	inWS := func(p string) bool {
		r := resolve(p)
		return under(r, ws) && !slices.Contains(strings.Split(r, string(filepath.Separator)), ".claude")
	}
	switch tool {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Glob", "Grep", "LS":
		var paths []string
		for _, k := range []string{"file_path", "notebook_path", "path"} {
			if p, _ := input[k].(string); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			paths = append(paths, cwd)
		}
		if tool == "Glob" {
			if p, _ := input["pattern"].(string); filepath.IsAbs(p) || strings.HasPrefix(p, "~") || strings.Contains(p, "..") {
				paths = append(paths, p)
			}
		}
		for _, p := range paths {
			if !inWS(p) {
				return no("this file")
			}
		}
	case "Bash":
		cmd, _ := input["command"].(string)
		c := strings.TrimSpace(cmd)
		if c == "edge" || strings.HasPrefix(c, "edge ") && !strings.ContainsAny(c, ";&|`$()<>\n\r\\") {
			return false, ""
		}
		if !inWS(cwd) {
			return no("the shell outside " + tilde(ws))
		}
		if strings.Contains(c, ".claude") {
			return no("this command")
		}
		for _, m := range cdRe.FindAllStringSubmatch(c, -1) {
			arg := strings.Trim(strings.TrimSpace(m[1]), `"'`)
			if arg == "" || arg == "-" || strings.Contains(arg, "..") || strings.ContainsAny(arg, "$`") || !inWS(arg) {
				return no("this command")
			}
		}
		for _, tok := range shellSplit.Split(cmd, -1) {
			if shellWords[tok] || tok == "." {
				return no("this command")
			}
			if !strings.ContainsAny(tok, "/~$") && !strings.Contains(tok, "..") {
				continue // plain words stay relative to the workspace
			}
			if strings.HasPrefix(tok, "$") && !strings.HasPrefix(tok, "$HOME") && !strings.HasPrefix(tok, "${HOME}") {
				return no("this command")
			}
			if strings.Contains(tok, "://") {
				continue // URLs
			}
			r := resolve(tok)
			if !inWS(tok) && !under(r, "/usr") && !under(r, "/tmp") && r != "/dev/null" {
				return no("this command")
			}
		}
	default:
		if !slices.Contains(safeTools, tool) {
			return no("this tool")
		}
	}
	return false, ""
}

// tutorContract keeps Claude from doing the work when asked directly in the
// learning workspace, outside /edge-stuck.
func tutorContract(s *State) string {
	c := "edge tutor mode. The user practices DSA in C++ and never types edge commands: you run them. " +
		"Never write the solution or reveal the key idea for their current problem except through the edge-stuck / edge-giveup skills. " +
		"Map plain talk to skills: stuck/hint/help → edge-stuck; got AC/accepted/solved → edge-done; give up/show solution → edge-giveup; " +
		"already solved this / different problem → edge-next (skip); progress/streak → edge-status. " +
		"General concepts, C++ syntax, STL and compiler errors are fine to explain. Never run `edge pass` or `edge action`: only the user can. " +
		"After a contest round: debrief → edge-round."
	if t := s.Current; t != nil {
		c += fmt.Sprintf(" Current problem: %s %s (%d hints used, %d rejected).", t.Title, t.URL, t.Hints, t.Fails)
		if t.Kind == "upsolve" {
			c += " It's an upsolve from " + t.RoundName + ": hints follow the ladder like a new problem."
		}
	}
	if t := s.lastSolved(); t != nil {
		c += fmt.Sprintf(" %s was accepted and still needs its explain-back (edge-done).", t.Title)
	}
	return c
}

func since(t time.Time) string {
	d := max(0, now().Sub(t)).Round(time.Minute)
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return dur(d)
}
