// edge keeps you at the edge of what you can solve. A daemon picks the next
// problem, watches your submissions and your screen, and speaks first; Claude
// is the tutor; a Claude Code gate holds the line until the day's work is done.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// out is where commands print. Commands that change state print into a
// buffer that's shown only once the change is saved: Claude must never teach
// a solution for a give-up that didn't stick.
var out io.Writer = os.Stdout

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "hook":
		hookCmd()
		return
	case "action": // Ember's buttons
		if len(args) != 1 {
			usage()
		}
		onAction(args[0])
		return
	case "install":
		check(install())
		return
	case "daemon":
		daemon()
		return
	case "status":
		s, err := load()
		check(err)
		fmt.Print(status(s))
		return
	case "report":
		s, err := load()
		check(err)
		fmt.Print(s.experimentReport())
		return
	case "stats":
		s, err := load()
		check(err)
		enc := json.NewEncoder(os.Stdout)
		if !slices.Contains(args, "--compact") {
			enc.SetIndent("", " ")
		}
		check(enc.Encode(s.stats()))
		return
	case "rounds":
		s, err := load()
		check(err)
		fmt.Print(roundsReport(s))
		return
	case "config":
		if len(args) == 1 && args[0] == "init" {
			check(writeDefaultConfig())
		}
		fmt.Print(configReport())
		return
	}

	// Network first, from a snapshot, without the lock.
	snap, err := load()
	check(err)
	var (
		cat       *catalog
		statement string
		syncs     []syncData
		verified  bool
	)
	switch cmd {
	case "next", "start":
		if snap.Current == nil {
			cat = loadCatalog()
		}
	case "skip":
		cat = loadCatalog()
	case "stuck", "giveup":
		if t := snap.Current; t != nil && t.Source() == "lc" {
			statement, _ = lcStatement(strings.TrimPrefix(t.ID, "lc:"))
		}
	case "done":
		if t := snap.Current; t != nil {
			ok, err := verifyAC(t)
			if err != nil {
				check(fmt.Errorf("couldn't verify with %s: %w", srcName(t.Source()), err))
			}
			verified = ok
		}
	case "sync":
		for _, src := range syncSources() {
			syncs = append(syncs, fetchSync(src, src == "cf"))
		}
	case "pass":
		check(passAllowed())
	}

	var buf bytes.Buffer
	out = &buf
	var after func()
	err = update(func(s *State) error {
		s.pin()
		switch cmd {
		case "sync":
			for _, d := range syncs {
				s.applySync(d)
				if d.Err != nil {
					return d.Err
				}
			}
			fmt.Fprintf(out, "synced: cf %d, lc %d, %d problems already solved on CF\n", s.Ratings["cf"], s.Ratings["lc"], len(s.CFDone))
			if r := s.Ratings["ac"]; r > 0 {
				fmt.Fprintf(out, "atcoder %d\n", r)
			}
		case "next", "start":
			if s.Current == nil {
				if cat == nil {
					cat = &catalog{err: map[string]error{"cf": fmt.Errorf("not loaded")}}
				}
				if _, err := assign(s, cat); err != nil {
					return err
				}
			}
			begin(s, s.Current, now())
			fmt.Fprint(out, describe(s, s.Current))
			t := *s.Current
			after = func() {
				if err := scaffold(&t); err != nil {
					fmt.Fprintln(os.Stderr, "edge: couldn't write the starter file:", err)
				}
				if cmd == "start" {
					launchWork(&t)
				}
			}
		case "stuck":
			take := 0
			if len(args) == 2 && args[0] == "--take" {
				n, err := strconv.Atoi(args[1])
				if err != nil {
					usage()
				}
				take = n
			}
			return stuck(s, take, statement)
		case "done":
			if s.Current != nil && snap.Current != nil && s.Current.ID != snap.Current.ID {
				return fmt.Errorf("the task changed while checking; run it again")
			}
			return done(s, verified)
		case "giveup":
			return giveup(s, statement)
		case "skip":
			return skip(s, slices.Contains(args, "--seen"), cat)
		case "explain":
			if len(args) == 0 {
				return explainInfo(s)
			}
			n, err := strconv.Atoi(args[0])
			if err != nil {
				usage()
			}
			return explain(s, n)
		case "pass":
			return pass(s)
		case "round":
			if len(args) >= 2 && args[0] == "lesson" {
				return setLesson(s, strings.Join(args[1:], " "))
			}
			if len(args) != 0 {
				usage()
			}
			return roundInfo(s)
		default:
			usage()
		}
		return nil
	})
	out = os.Stdout
	check(err)
	os.Stdout.Write(buf.Bytes())
	if after != nil {
		after()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: edge daemon|hook|action <key>|install|status|stats [--compact]|config [init]|next|start|stuck [--take N]|done|giveup|skip [--seen]|explain [0-2]|rounds|round [lesson <text>]|report|pass|sync")
	os.Exit(2)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "edge:", err)
		os.Exit(1)
	}
}

// passAllowed: the weekly pass is the user's call, typed by them in a real
// terminal. Claude (or anything it starts) can't spend it.
func passAllowed() error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return fmt.Errorf("`edge pass` only works typed in a terminal")
	}
	for pid := os.Getppid(); pid > 1; {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			break
		}
		// pid (comm) state ppid ...
		st := string(b)
		open, close := strings.IndexByte(st, '('), strings.LastIndexByte(st, ')')
		if open < 0 || close < open {
			break
		}
		if comm := st[open+1 : close]; comm == "claude" {
			return fmt.Errorf("`edge pass` can't be run from Claude; type it in your own terminal")
		}
		f := strings.Fields(st[close+1:])
		if len(f) < 2 {
			break
		}
		pid, _ = strconv.Atoi(f[1])
	}
	return nil
}

func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return e == 0
}

func pass(s *State) error {
	if s.passOn(now()) {
		return fmt.Errorf("already passed today")
	}
	if s.passUsedThisWeek() {
		return fmt.Errorf("this week's pass is used; do one task")
	}
	s.Passes = append(s.Passes, now())
	fmt.Fprintln(out, "gate open for today. It's logged.")
	return nil
}

// status is read-only: looking never changes anything.
func status(s *State) string {
	var b strings.Builder
	got, need := s.today()
	fmt.Fprintf(&b, "today %d/%d · week %d/%d · streak %d days\n", got, need, s.creditsSince(weekStart(now())), cfg().Weekly, s.streak())
	for _, src := range cfg().Sources {
		lo, hi := s.window(src)
		fmt.Fprintf(&b, "%s rating %d → working band %d-%d (offset %+d)\n", src, s.Ratings[src], lo, hi, s.Offset[src])
	}
	if t := s.Current; t != nil {
		b.WriteString("current: ")
		b.WriteString(describe(s, t))
	}
	due := 0
	for _, p := range s.Probes {
		if !p.Due.After(now()) {
			due++
		}
	}
	fmt.Fprintf(&b, "re-solves: %d due, %d scheduled\n", due, len(s.Probes))
	if r := s.liveHeld(); r != nil {
		fmt.Fprintf(&b, "ROUND LIVE: %s, %s left · solved %s\n", r.Name, r.clock(now()), orNone(r.letters("solved")))
	} else if r := s.nextHeld(); r != nil {
		fmt.Fprintf(&b, "next round: %s, %s (you're in)\n", r.Name, clockDay(r.Start))
	}
	if r, p := s.nextUpsolve(); p != nil {
		fmt.Fprintf(&b, "upsolve next: %s %s %s, by %s\n", r.Short(), p.Index, p.Title, p.Due.Add(-time.Minute).Format("Mon"))
	}
	b.WriteString(gateLine(s))
	if t := mtime(daemonSeenPath()); t.IsZero() || time.Since(t) > 2*time.Minute {
		b.WriteString("DAEMON IS NOT RUNNING: nothing is offered or tracked. `systemctl --user start edge`\n")
	}
	for _, k := range slices.Sorted(mapsKeys(s.Trouble)) {
		fmt.Fprintf(&b, "trouble (%s, since %s): %s\n", k, s.Trouble[k].Since.Format("Mon 15:04"), s.Trouble[k].Msg)
	}
	b.WriteString(s.experimentReport())
	return b.String()
}

func gateLine(s *State) string {
	if !gateInstalled() {
		return "GATE IS OFF: the hook is missing from ~/.claude/settings.json, or hooks are disabled. Run `edge install`.\n"
	}
	seen := ""
	if t := gateSeen(); !t.IsZero() {
		seen = " · hook last ran " + since(t) + " ago"
	}
	got, need := s.today()
	switch {
	case s.closed():
		return "gate: closed until today's work is done" + seen + "\n"
	case !s.rules().Gate:
		return "gate: disabled in config" + seen + "\n"
	case s.GateFrom.IsZero() || now().Before(s.GateFrom):
		return "gate: not enforcing yet" + seen + "\n"
	case got >= need:
		return "gate: open, today's done" + seen + "\n"
	case s.passOn(now()):
		return "gate: open, pass used today" + seen + "\n"
	case s.failOpen() != "":
		return "gate: open because edge can't offer work: " + s.failOpen() + seen + "\n"
	case s.roundToday() != nil:
		return "gate: open until today's round, " + s.roundToday().Short() + " at " + s.roundToday().Start.Format("15:04") + seen + "\n"
	default:
		return "gate: open outside study time" + seen + "\n"
	}
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func configReport() string {
	c := cfg()
	var b strings.Builder
	p := configPath()
	if exists(p) {
		fmt.Fprintf(&b, "config: %s\n", p)
	} else {
		fmt.Fprintf(&b, "config: %s (not written yet; defaults in use, `edge install` writes it)\n", p)
	}
	for _, e := range confErrs {
		fmt.Fprintf(&b, "  ignored: %s\n", e)
	}
	fmt.Fprintf(&b, "accounts: cf %s, lc %s\n", c.CFUser, c.LCUser)
	fmt.Fprintf(&b, "study time from %s weekdays, %s weekends; days end %s\n", hm(c.WeekdayFrom), hm(c.WeekendFrom), hm(c.DayEnds))
	fmt.Fprintf(&b, "quota %d weekday, %d weekend, %d a week\n", c.WeekdayMin, c.WeekendMin, c.Weekly)
	fmt.Fprintf(&b, "gate %v, workspace %s\n", c.Gate, tilde(c.Workspace))
	if s, err := load(); err == nil {
		if r := s.rules(); r.WeekdayMin != c.WeekdayMin || r.WeekendMin != c.WeekendMin || r.Gate != c.Gate || r.Workspace != c.Workspace {
			fmt.Fprintf(&b, "today keeps the rules it started with (quota %d/%d, gate %v, workspace %s); your changes apply tomorrow\n",
				r.WeekdayMin, r.WeekendMin, r.Gate, tilde(r.Workspace))
		}
	}
	return b.String()
}

func hm(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

func settingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "settings.json")
}

// gateInstalled: the settings carry edge's hook on both events and hooks
// aren't switched off.
func gateInstalled() bool {
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		return false
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil {
		return false
	}
	if off, _ := cfg["disableAllHooks"].(bool); off {
		return false
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	for _, ev := range []string{"UserPromptSubmit", "PreToolUse"} {
		list, _ := hooks[ev].([]any)
		if !slices.ContainsFunc(list, isEdgeHook) {
			return false
		}
	}
	return true
}
