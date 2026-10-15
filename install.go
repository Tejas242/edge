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
	if strings.Contains(exe, "go-build") || strings.HasPrefix(exe, os.TempDir()) {
		return fmt.Errorf("%s is a temporary binary (go run?): build it to ~/.local/bin/edge and install from there", exe)
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

	if p := configPath(); !exists(p) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(defaultConfigText), 0o644); err != nil {
			return err
		}
		fmt.Println("config →", p)
	} else {
		fmt.Println("config →", p, "(kept)")
	}

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
	if err := writeAtomic(settingsPath(), out, 0o644); err != nil {
		return err
	}
	fmt.Println("gate hook →", settingsPath(), "(backup: settings.json.pre-edge)")
	err = update(func(s *State) error {
		if s.GateFrom.IsZero() {
			s.GateFrom = day(now()).AddDate(0, 0, 1)
		}
		fmt.Println("gate enforces from", s.GateFrom.Format("Mon 02 Jan"))
		return nil
	})
	if err != nil {
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
# windows edge opens (editor, Claude) must outlive a restart or update
KillMode=process
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
	}
	for _, u := range units {
		if err := os.WriteFile(filepath.Join(dir, u.name), []byte(fmt.Sprintf(unit, u.desc, exe, u.cmd)), 0o644); err != nil {
			return err
		}
	}
	// the standalone island is now the Ember plugin
	exec.Command("systemctl", "--user", "disable", "--now", "edge-island.service").Run()
	os.Remove(filepath.Join(dir, "edge-island.service"))
	steps := [][]string{{"daemon-reload"}, {"enable", "edge.service"}, {"restart", "edge.service"}}
	for _, args := range steps {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %v: %v: %s", args, err, out)
		}
	}
	fmt.Println("service → edge.service (running)")
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
		list, ok := v.([]any)
		if !ok {
			continue // not ours, and not a shape we understand: leave it alone
		}
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
	add := func(event, matcher, command string, timeout int, async bool) {
		h := map[string]any{"type": "command", "command": command, "timeout": timeout}
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
	add("UserPromptSubmit", "", hookCmd, 10, false) // gate
	add("PreToolUse", "*", hookCmd, 10, false)      // gate on tools
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

// edgeCmd matches edge's own hook commands from any install path, quoted or not.
var edgeCmd = regexp.MustCompile(`^(?:"[^"]*/edge"|'[^']*/edge'|\S*/edge|edge)\s+(?:gate|gate-tool|hook)(?:\s|$)`)

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
