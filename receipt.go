package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// expiredWhy is why a card Ember never showed (or showed too briefly) went
// away: the words Ember's receipts use.
var expiredWhy = []string{"unseen", "fullscreen", "away", "osd", "replaced"}

// receipt records what Ember says happened to a card edge pushed: `edge
// receipt seen <cardkey> [visible_ms]` or `edge receipt expired <cardkey>
// [why]`. It only appends one event. No state, no lock, no push back to
// Ember (a receipt must never cause a card), no network. A card key edge
// doesn't know any more is logged all the same: the log is the evidence.
// It's a command of its own, not an `action` key, because card keys hold
// colons ("offer:cf:1418C:2026-11-01:1") and a key:suffix form can't be
// split safely; it also keeps receipts out of act()'s double-click guard,
// the "action" event and the status push.
func receipt(args []string) error {
	if len(args) < 2 || len(args) > 3 {
		return fmt.Errorf("usage: edge receipt seen <cardkey> [visible_ms] | expired <cardkey> [%s]", strings.Join(expiredWhy, "|"))
	}
	kind, key := args[0], args[1]
	if key == "" || len(key) > 200 || strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%q isn't a card key", key)
	}
	kv := map[string]any{"key": key}
	switch kind {
	case "seen":
		if len(args) == 3 {
			ms, err := strconv.Atoi(args[2])
			if err != nil || ms < 0 {
				return fmt.Errorf("visible time %q should be milliseconds", args[2])
			}
			kv["ms"] = ms
		}
		logEvent("card_seen", kv)
	case "expired":
		if len(args) == 3 {
			if !slices.Contains(expiredWhy, args[2]) {
				return fmt.Errorf("%q isn't a reason; use %s", args[2], strings.Join(expiredWhy, ", "))
			}
			kv["why"] = args[2]
		}
		logEvent("card_expired", kv)
	default:
		return fmt.Errorf("receipt is seen or expired, not %q", kind)
	}
	return nil
}
