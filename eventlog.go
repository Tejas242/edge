package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// eventLogMax is where events.jsonl rotates to events.jsonl.1.
const eventLogMax = 1 << 20

func eventLogPath() string { return filepath.Join(dataDir(), "events.jsonl") }

// logEvent appends one JSON line to ~/.local/share/edge/events.jsonl: what
// edge showed and what he did (shown, start, abandon, skip, giveup, verdict,
// action, action_error …), for looking back at how the evenings went. One
// open-append-close per event; past 1 MB the file rotates. Best effort: a
// log that can't be written never stops edge.
func logEvent(kind string, kv map[string]any) {
	line := make(map[string]any, len(kv)+2)
	for k, v := range kv {
		line[k] = v
	}
	line["at"], line["ev"] = now().Format(time.RFC3339), kind
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	p := eventLogPath()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if st, err := f.Stat(); err == nil && st.Size()+int64(len(b)) >= eventLogMax {
		f.Close()
		os.Rename(p, p+".1")
		if f, err = os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			return
		}
	}
	f.Write(append(b, '\n'))
	f.Close()
}
