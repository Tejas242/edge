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
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	addHook := func(event, matcher, command string) {
		list, _ := hooks[event].([]any)
		if strings.Contains(fmt.Sprint(list), command) {
			return
		}
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 10}}}
		if matcher != "" {
			entry["matcher"] = matcher
		}
		hooks[event] = append(list, entry)
	}
	addHook("UserPromptSubmit", "", exe+" gate")
	addHook("PreToolUse", "*", exe+" gate-tool")
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
		return err
	}
	if err := os.WriteFile(settingsPath(), out.Bytes(), 0o644); err != nil {
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
Description=edge: picks problems, watches submissions, speaks first
PartOf=graphical-session.target
After=graphical-session.target

[Service]
ExecStart=%s daemon
Restart=always
RestartSec=10
Environment=PATH=%%h/.local/bin:/usr/local/bin:/usr/bin

[Install]
WantedBy=graphical-session.target
`

func installService(exe string) error {
	h, _ := os.UserHomeDir()
	p := filepath.Join(h, ".config", "systemd", "user", "edge.service")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(fmt.Sprintf(unit, exe)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "edge.service"}, {"restart", "edge.service"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %v: %v: %s", args, err, out)
		}
	}
	fmt.Println("daemon →", p, "(running)")
	return nil
}
