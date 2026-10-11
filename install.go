package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	if err := os.WriteFile(settingsPath()+".pre-edge", raw, 0o644); err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if !gateInstalled() {
		hooks, _ := cfg["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		ups, _ := hooks["UserPromptSubmit"].([]any)
		hooks["UserPromptSubmit"] = append(ups, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": exe + " gate", "timeout": 10}},
		})
		cfg["hooks"] = hooks
	}
	perms, _ := cfg["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	if !slices.Contains(allow, any("Bash(edge:*)")) {
		perms["allow"] = append(allow, "Bash(edge:*)")
	}
	cfg["permissions"] = perms
	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(settingsPath(), append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("gate hook →", settingsPath(), "(backup: settings.json.pre-edge)")
	return nil
}
