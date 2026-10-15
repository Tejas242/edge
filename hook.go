package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// hookCmd is edge's Claude Code hook: the gate. (Sessions, approvals and
// questions are Ember's: the Noctalia plugin registers its own hook.)
//
// It must never lock Claude Code out by accident: it reads the state without
// the lock (writes are atomic renames), never writes it, and a panic or any
// error means "allow". Exit code 2 would mean "block", so it always exits 0.
func hookCmd() {
	defer func() {
		recover()
		os.Exit(0)
	}()
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	var in struct {
		Event  string         `json:"hook_event_name"`
		Prompt string         `json:"prompt"`
		Cwd    string         `json:"cwd"`
		Tool   string         `json:"tool_name"`
		Input  map[string]any `json:"tool_input"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return
	}
	s, err := load()
	if err != nil {
		return // fail open: a broken state file must not lock you out of Claude
	}
	out := json.NewEncoder(os.Stdout)
	switch in.Event {
	case "UserPromptSubmit":
		touchGateSeen()
		v := gateDecide(s, in.Prompt, in.Cwd)
		if v.Block {
			out.Encode(map[string]any{"decision": "block", "reason": v.Reason})
		} else if v.Context != "" {
			out.Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "UserPromptSubmit", "additionalContext": v.Context}})
		}
	case "PreToolUse":
		if deny, reason := gateTool(s, in.Tool, in.Input, in.Cwd); deny {
			out.Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
		}
	}
}

// The hook's heartbeat is a file's mtime, not a state write: the hook stays
// read-only and never waits on the daemon.
func gateSeenPath() string { return filepath.Join(dataDir(), "gate.seen") }

func touchGateSeen() { touch(gateSeenPath()) }

func gateSeen() time.Time { return mtime(gateSeenPath()) }

// The daemon's heartbeat, the same way: status can tell it's alive without
// the daemon rewriting state.json every tick.
func daemonSeenPath() string { return filepath.Join(dataDir(), "daemon.seen") }

func touch(p string) {
	n := time.Now()
	if os.Chtimes(p, n, n) != nil {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644)
	}
}

func mtime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}
