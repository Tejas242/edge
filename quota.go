package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	weekdayMin   = 1
	weekendMin   = 3
	weeklyTarget = 12
	honestTry    = 45 * time.Minute // a give-up after this long still earns the day
)

func day(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func weekStart(t time.Time) time.Time {
	d := day(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // Monday
}

func dailyMin(t time.Time) int {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return weekendMin
	}
	return weekdayMin
}

func credit(t Task) int {
	switch t.Outcome {
	case "solo", "assisted":
		return 1
	case "gaveup":
		if t.Finished.Sub(t.Started) >= honestTry {
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
	return n
}

func (s *State) today() (got, need int) {
	return s.creditsSince(day(now())), dailyMin(now())
}

// streak counts consecutive days, ending today or yesterday, that met the minimum.
func (s *State) streak() int {
	d := day(now())
	if got, need := s.today(); got < need {
		d = d.AddDate(0, 0, -1)
	}
	n := 0
	for {
		got := 0
		for _, t := range s.History {
			if day(t.Finished).Equal(d) {
				got += credit(t)
			}
		}
		if got < dailyMin(d) && !s.passOn(d) {
			return n
		}
		n++
		d = d.AddDate(0, 0, -1)
	}
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

func workDir() string {
	if d := os.Getenv("EDGE_WORK"); d != "" {
		return d // tests: never touch the real ~/CP
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "CP")
}

type verdict struct {
	Block   bool
	Reason  string
	Context string // injected into Claude's context when allowed
}

// closed: today's minimum isn't met and nothing excuses it.
func (s *State) closed() bool {
	got, need := s.today()
	return got < need && !s.passOn(now()) && !now().Before(s.GateFrom)
}

func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// gateDecide is the prompt half of the lockout. While closed, only practice
// gets through: prompts from ~/CP or edge's own skills, with a contract that
// keeps Claude on practice. gateTool enforces the same thing on tool calls.
func gateDecide(s *State, prompt, cwd string) verdict {
	p := strings.TrimSpace(prompt)
	practice := under(cwd, workDir()) || strings.HasPrefix(p, "/edge")
	if !s.closed() {
		if under(cwd, workDir()) {
			return verdict{Context: tutorContract(s)}
		}
		return verdict{}
	}
	if practice {
		return verdict{Context: tutorContract(s) + " " + lockedContract}
	}
	got, need := s.today()
	var b strings.Builder
	fmt.Fprintf(&b, "edge gate: today's learning isn't done (%d/%d).", got, need)
	if t := s.Current; t != nil && t.Active() {
		fmt.Fprintf(&b, "\nYou're on %s (%s), %s in.", t.Title, t.URL, since(t.Started))
	} else if t != nil {
		fmt.Fprintf(&b, "\nWaiting for you: %s (%s). Click Start on the notification, or `edge start`.", t.Title, t.URL)
	}
	b.WriteString("\nTalk to Claude from ~/CP about it any time.")
	if !s.passUsedThisWeek() {
		b.WriteString("\nReal emergency? `edge pass` unlocks today (1 per week).")
	}
	return verdict{Block: true, Reason: b.String()}
}

const lockedContract = "GATE CLOSED: today's practice isn't done. Only help with competitive programming / DSA practice. " +
	"Decline anything else (other projects, deploys, config) with one line: it unlocks after today's problem. " +
	"Never offer to disable, edit or work around the edge gate or its hooks."

var (
	shellSplit = regexp.MustCompile(`[\s'"=;&|<>()` + "`" + `]+`)
	bareCd     = regexp.MustCompile(`(^|[;&|(]\s*)(cd|pushd)\s*($|[;&|)])`)
)

// gateTool is the hard half: while closed, tools may only touch ~/CP.
func gateTool(s *State, tool string, input map[string]any, cwd string) (deny bool, reason string) {
	if !s.closed() {
		return false, ""
	}
	h, _ := os.UserHomeDir()
	no := func(what string) (bool, string) {
		return true, "edge gate: " + what + " is locked until today's practice is done. " +
			"Only ~/CP is open. Tell the user it unlocks after today's problem; never offer to disable or edit the gate."
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
	switch tool {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Glob", "Grep", "LS":
		p, _ := input["file_path"].(string)
		if p == "" {
			p, _ = input["notebook_path"].(string)
		}
		if p == "" {
			p, _ = input["path"].(string)
		}
		if p == "" {
			p = cwd
		}
		if !under(resolve(p), workDir()) {
			return no("this file")
		}
	case "Bash":
		cmd, _ := input["command"].(string)
		c := strings.TrimSpace(cmd)
		if c == "edge" || strings.HasPrefix(c, "edge ") && !strings.ContainsAny(c, ";&|`$()<>") {
			return false, ""
		}
		if !under(cwd, workDir()) {
			return no("the shell outside ~/CP")
		}
		if bareCd.MatchString(c) {
			return no("this command")
		}
		for _, tok := range shellSplit.Split(cmd, -1) {
			if !strings.ContainsAny(tok, "/~$") {
				continue // plain words stay relative to ~/CP
			}
			if strings.HasPrefix(tok, "$") && !strings.HasPrefix(tok, "$HOME") && !strings.HasPrefix(tok, "${HOME}") {
				return no("this command")
			}
			if strings.Contains(tok, "://") {
				continue // URLs
			}
			r := resolve(tok)
			if !under(r, workDir()) && !under(r, "/usr") && !under(r, "/tmp") && r != "/dev/null" {
				return no("this command")
			}
		}
	case "WebFetch", "WebSearch", "Skill", "Task", "Agent", "TodoWrite", "ToolSearch":
	default:
		if strings.HasPrefix(tool, "mcp__") {
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
		"General concepts, C++ syntax, STL and compiler errors are fine to explain."
	if t := s.Current; t != nil {
		c += fmt.Sprintf(" Current problem: %s %s (policy %s, %d hints used, %d rejected).", t.Title, t.URL, t.Policy, t.Hints, t.Fails)
	}
	if t := s.lastSolved(); t != nil {
		c += fmt.Sprintf(" %s was accepted and still needs its explain-back (edge-done).", t.Title)
	}
	return c
}

func since(t time.Time) string {
	d := now().Sub(t).Round(time.Minute)
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
