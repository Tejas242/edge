package main

// logEvent appends one line to edge's event log (contract v2: start, abandon,
// verdict, cue, action, error …). fix/core implements it; this stub keeps
// every branch compiling.
func logEvent(kind string, kv map[string]any) {}
