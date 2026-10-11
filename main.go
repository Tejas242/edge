// edge keeps you at the edge of what you can solve. A daemon picks the next
// problem, watches your submissions and your screen, and speaks first; Claude
// is the tutor; a Claude Code gate holds the line until the day's work is done.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Tejas242/edge/island"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "gate":
		gate() // must never fail closed on its own errors
		return
	case "gate-tool":
		gateToolHook()
		return
	case "hook":
		hookCmd()
		return
	case "island":
		if err := island.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "edge island:", err)
			os.Exit(1)
		}
		return
	case "install":
		check(install())
		return
	case "daemon":
		daemon()
		return
	}
	check(update(func(s *State) error {
		switch cmd {
		case "sync":
			if err := sync(s); err != nil {
				return err
			}
			fmt.Printf("synced: cf %d, lc %d, %d problems already solved on CF\n", s.Ratings["cf"], s.Ratings["lc"], len(s.CFDone))
		case "next":
			if s.Current == nil {
				if _, err := assign(s); err != nil {
					return err
				}
			}
			begin(s.Current, now())
			fmt.Print(describe(s, s.Current))
		case "start": // what the notification's Start button does
			if s.Current == nil {
				if _, err := assign(s); err != nil {
					return err
				}
			}
			begin(s.Current, now())
			launchWork(s.Current)
		case "stuck":
			return stuck(s)
		case "done":
			return done(s)
		case "giveup":
			return giveup(s)
		case "skip":
			return skip(s, slices.Contains(args, "--seen"))
		case "explain":
			if len(args) == 0 {
				return explainInfo(s)
			}
			n, err := strconv.Atoi(args[0])
			if err != nil {
				usage()
			}
			return explain(s, n)
		case "status":
			fmt.Print(status(s))
		case "report":
			fmt.Print(s.experimentReport())
		case "pass":
			return pass(s)
		default:
			usage()
		}
		return nil
	}))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: edge daemon|island|hook|install|status|next|start|stuck|done|giveup|skip [--seen]|explain [0-2]|report|pass|sync|gate")
	os.Exit(2)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "edge:", err)
		os.Exit(1)
	}
}

func sync(s *State) error {
	r, err := cfRating()
	if err != nil {
		return err
	}
	s.Ratings["cf"] = r
	subs, err := cfSubs(0)
	if err != nil {
		return err
	}
	for _, x := range subs {
		k := x.Problem.key()
		s.CFTried[k] = true
		if x.Verdict == "OK" {
			if at, ok := s.CFDone[k]; !ok || x.Time < at {
				s.CFDone[k] = x.Time
			}
		}
	}
	lr, recent, err := lcProfile()
	if err != nil {
		return err
	}
	if lr > 0 {
		s.Ratings["lc"] = lr
	}
	for _, a := range recent {
		s.Seen["lc:"+a.Slug] = true
	}
	s.LastSync = now()
	return nil
}

func pass(s *State) error {
	if s.passOn(now()) {
		return fmt.Errorf("already passed today")
	}
	if s.passUsedThisWeek() {
		return fmt.Errorf("this week's pass is used; do one task")
	}
	s.Passes = append(s.Passes, now())
	fmt.Println("gate open for today. It's logged.")
	return nil
}

func status(s *State) string {
	var b strings.Builder
	got, need := s.today()
	fmt.Fprintf(&b, "today %d/%d · week %d/%d · streak %d days\n", got, need, s.creditsSince(weekStart(now())), weeklyTarget, s.streak())
	for _, src := range []string{"cf", "lc"} {
		lo, hi := s.window(src)
		fmt.Fprintf(&b, "%s rating %d → working band %d-%d\n", src, s.Ratings[src], lo, hi)
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
	if !gateInstalled() {
		s.GateOff = append(s.GateOff, now())
		fmt.Fprintf(&b, "GATE IS OFF: the hook is missing from ~/.claude/settings.json (seen off %d times)\n", len(s.GateOff))
	} else if !s.GateLast.IsZero() {
		fmt.Fprintf(&b, "gate: last ran %s ago\n", since(s.GateLast))
	}
	b.WriteString(s.experimentReport())
	return b.String()
}

// ---- hook ----

func gate() {
	var in struct {
		Prompt string `json:"prompt"`
		Cwd    string `json:"cwd"`
	}
	json.NewDecoder(os.Stdin).Decode(&in)
	var v verdict
	err := update(func(s *State) error {
		v = gateDecide(s, in.Prompt, in.Cwd)
		s.GateRuns++
		s.GateLast = now()
		return nil
	})
	if err != nil {
		return // fail open: a broken state file must not lock you out of Claude
	}
	switch {
	case v.Block:
		json.NewEncoder(os.Stdout).Encode(map[string]any{"decision": "block", "reason": v.Reason})
	case v.Context != "":
		json.NewEncoder(os.Stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "UserPromptSubmit", "additionalContext": v.Context}})
	}
}

func gateToolHook() {
	var in struct {
		Tool  string         `json:"tool_name"`
		Input map[string]any `json:"tool_input"`
		Cwd   string         `json:"cwd"`
	}
	json.NewDecoder(os.Stdin).Decode(&in)
	s, err := load() // read-only, no lock: this runs before every tool call
	if err != nil {
		return
	}
	if deny, reason := gateTool(s, in.Tool, in.Input, in.Cwd); deny {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
	}
}

func settingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "settings.json")
}

func gateInstalled() bool {
	b, err := os.ReadFile(settingsPath())
	return err == nil && strings.Contains(string(b), "edge hook")
}
