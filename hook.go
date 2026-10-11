package main

import (
	"encoding/json"
	"io"
	"os"

	"github.com/Tejas242/edge/island"
)

// hookCmd is the single entry point for every Claude Code hook: the gate
// first, then the island (which answers permissions and questions).
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
			return
		}
		island.Send(in.Event, raw)
		if v.Context != "" {
			out.Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "UserPromptSubmit", "additionalContext": v.Context}})
		}
	case "PreToolUse":
		if s, err := load(); err == nil {
			if deny, reason := gateTool(s, in.Tool, in.Input, in.Cwd); deny {
				out.Encode(map[string]any{"hookSpecificOutput": map[string]any{
					"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
				return
			}
		}
		if in.Tool == "AskUserQuestion" {
			if ans := island.Ask(in.Event, raw); ans != nil {
				os.Stdout.Write(ans)
			}
			return // no answer: Claude asks in the terminal
		}
		island.Send(in.Event, raw)
	case "PermissionRequest":
		if ans := island.Ask(in.Event, raw); ans != nil {
			os.Stdout.Write(ans)
		}
	default:
		island.Send(in.Event, raw)
	}
}
