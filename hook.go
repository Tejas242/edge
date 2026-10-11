package main

import (
	"encoding/json"
	"io"
	"os"
)

// hookCmd is edge's Claude Code hook: the gate. (Sessions, approvals and
// questions are Ember's: the Noctalia plugin registers its own hook.)
func hookCmd() {
	raw, _ := io.ReadAll(os.Stdin)
	var in struct {
		Event  string         `json:"hook_event_name"`
		Prompt string         `json:"prompt"`
		Cwd    string         `json:"cwd"`
		Tool   string         `json:"tool_name"`
		Input  map[string]any `json:"tool_input"`
	}
	json.Unmarshal(raw, &in)
	out := json.NewEncoder(os.Stdout)
	switch in.Event {
	case "UserPromptSubmit":
		var v verdict
		if err := update(func(s *State) error {
			v = gateDecide(s, in.Prompt, in.Cwd)
			s.GateRuns++
			s.GateLast = now()
			return nil
		}); err != nil {
			return // fail open
		}
		if v.Block {
			out.Encode(map[string]any{"decision": "block", "reason": v.Reason})
		} else if v.Context != "" {
			out.Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "UserPromptSubmit", "additionalContext": v.Context}})
		}
	case "PreToolUse":
		s, err := load()
		if err != nil {
			return
		}
		if deny, reason := gateTool(s, in.Tool, in.Input, in.Cwd); deny {
			out.Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
		}
	}
}
