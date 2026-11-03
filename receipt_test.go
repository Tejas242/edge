package main

import (
	"os"
	"strings"
	"testing"
)

func TestReceiptsAreLoggedAndNothingElse(t *testing.T) {
	actState(t)
	os.Remove(eventLogPath())
	before, _ := load()
	key := "offer:cf:1418C:2026-11-01:1"
	for _, a := range [][]string{{"seen", key, "4200"}, {"seen", "gone:card"}, {"expired", key, "fullscreen"}, {"expired", key}} {
		if err := receipt(a); err != nil {
			t.Fatalf("%v: %v", a, err)
		}
	}
	b, _ := os.ReadFile(eventLogPath())
	log := string(b)
	for _, want := range []string{
		`"ev":"card_seen"`, `"key":"offer:cf:1418C:2026-11-01:1"`, `"ms":4200`, `"ev":"card_expired"`, `"why":"fullscreen"`, `"key":"gone:card"`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("event log lacks %s:\n%s", want, log)
		}
	}
	if strings.Contains(log, `"ev":"action"`) || strings.Contains(log, `"ev":"shown"`) || strings.Contains(log, `"ev":"cue_shown"`) {
		t.Errorf("a receipt logged more than itself:\n%s", log)
	}
	if strings.Count(log, "\n") != 4 {
		t.Errorf("want 4 events:\n%s", log)
	}
	after, _ := load()
	if before.Current.ID != after.Current.ID || before.LastAct != after.LastAct {
		t.Error("a receipt changed state")
	}
}

func TestMalformedReceiptsAreRefused(t *testing.T) {
	actionEnv(t)
	os.Remove(eventLogPath())
	for _, a := range [][]string{
		nil, {"seen"}, {"seen", ""}, {"seen", "a b"}, {"seen", "k", "-1"}, {"seen", "k", "soon"},
		{"expired", "k", "because"}, {"gazed", "k"}, {"seen", "k", "1", "2"},
	} {
		if err := receipt(a); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
	if _, err := os.Stat(eventLogPath()); err == nil {
		t.Error("a refused receipt wrote to the log")
	}
}
