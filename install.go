package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

//go:embed skills
var skills embed.FS

// install copies the skills into ~/.claude/skills and adds the gate hook to
// ~/.claude/settings.json (backed up first).
func install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	h, _ := os.UserHomeDir()
	dst := filepath.Join(h, ".claude", "skills")
	err = fs.WalkDir(skills, "skills", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := skills.ReadFile(p)
		out := filepath.Join(dst, p[len("skills/"):])
		os.MkdirAll(filepath.Dir(out), 0o755)
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return err
	}
	fmt.Println("skills →", dst)

	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		return err
	}
	if exists(settingsPath() + ".pre-edge") {
		// keep the first, pre-edge backup
	} else if err := os.WriteFile(settingsPath()+".pre-edge", raw, 0o644); err != nil {
		return err
	}
	out, err := patchSettings(raw, exe)
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath(), out, 0o644); err != nil {
		return err
	}
	fmt.Println("gate hook →", settingsPath(), "(backup: settings.json.pre-edge)")
	s, err := load()
	if err != nil {
		return err
	}
	if s.GateFrom.IsZero() {
		s.GateFrom = day(now()).AddDate(0, 0, 1)
	}
	fmt.Println("gate enforces from", s.GateFrom.Format("Mon 02 Jan"))
	if err := s.save(); err != nil {
		return err
	}
	return installService(exe)
}

const unit = `[Unit]
Description=%s
PartOf=graphical-session.target
After=graphical-session.target

[Service]
ExecStart=%s %s
Restart=always
RestartSec=10
Environment=PATH=%%h/.local/bin:/usr/local/bin:/usr/bin

[Install]
WantedBy=graphical-session.target
`

func installService(exe string) error {
	h, _ := os.UserHomeDir()
	dir := filepath.Join(h, ".config", "systemd", "user")
	os.MkdirAll(dir, 0o755)
	units := []struct{ name, desc, cmd string }{
		{"edge.service", "edge: picks problems, watches submissions, speaks first", "daemon"},
		{"edge-island.service", "edge island: Claude Code sessions and edge under the bar", "island"},
	}
	for _, u := range units {
		if err := os.WriteFile(filepath.Join(dir, u.name), []byte(fmt.Sprintf(unit, u.desc, exe, u.cmd)), 0o644); err != nil {
			return err
		}
	}
	steps := [][]string{{"daemon-reload"}, {"enable", "edge.service", "edge-island.service"}, {"restart", "edge-island.service", "edge.service"}}
	for _, args := range steps {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %v: %v: %s", args, err, out)
		}
	}
	fmt.Println("services → edge.service, edge-island.service (running)")
	return nil
}

// patchSettings adds edge's hooks and permission to Claude Code's settings,
// replacing any older edge entries and leaving everything else as written.
func patchSettings(raw []byte, exe string) ([]byte, error) {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	hookCmd := exe + " hook"
	// drop older edge entries (gate, gate-tool) and re-add one per event
	for event, v := range hooks {
		list, _ := v.([]any)
		kept := list[:0]
		for _, entry := range list {
			if !isEdgeHook(entry) {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	add := func(event, matcher string, timeout int, async bool) {
		h := map[string]any{"type": "command", "command": hookCmd, "timeout": timeout}
		if async {
			h["async"] = true
		}
		entry := map[string]any{"hooks": []any{h}}
		if matcher != "" {
			entry["matcher"] = matcher
		}
		list, _ := hooks[event].([]any)
		hooks[event] = append(list, entry)
	}
	add("UserPromptSubmit", "", 10, false)    // gate
	add("PreToolUse", "*", 100, false)        // gate + questions on the island
	add("PermissionRequest", "*", 100, false) // Allow / Deny / Always on the island
	for _, ev := range []string{"PostToolUse", "Notification", "Stop", "SessionStart", "SessionEnd"} {
		add(ev, map[string]string{"PostToolUse": "*"}[ev], 5, true) // just reporting: never block Claude
	}
	cfg["hooks"] = hooks
	perms, _ := cfg["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	if !slices.Contains(allow, any("Bash(edge:*)")) {
		perms["allow"] = append(allow, "Bash(edge:*)")
	}
	cfg["permissions"] = perms
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // keep <, >, & in existing settings as written
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var edgeCmd = regexp.MustCompile(`(^|/)edge (gate|gate-tool|hook)$`)

// isEdgeHook reports whether a settings hook entry runs edge (any install path).
func isEdgeHook(entry any) bool {
	m, _ := entry.(map[string]any)
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		hm, _ := h.(map[string]any)
		if c, _ := hm["command"].(string); edgeCmd.MatchString(strings.TrimSpace(c)) {
			return true
		}
	}
	return false
}
