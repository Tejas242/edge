package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	gosync "sync"
	"syscall"
)

var (
	slotMu gosync.Mutex
	slotID = map[string]string{} // slot → notification id on screen, for replacing
)

// notify shows e via notify-send; a clicked button's key goes to acts.
func notify(e event, acts chan<- string) {
	args := []string{"-a", "edge", "-p"}
	if e.Urgent {
		args = append(args, "-u", "critical")
	}
	slotMu.Lock()
	if id := slotID[e.slot()]; id != "" {
		args = append(args, "-r", id)
	}
	slotMu.Unlock()
	if len(e.Actions) > 0 {
		args = append(args, "--wait")
		for _, a := range e.Actions {
			args = append(args, "-A", a.Key+"="+a.Label)
		}
	}
	args = append(args, e.Title, e.Body)
	cmd := exec.Command("notify-send", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		log.Println("notify:", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Println("notify:", err)
		return
	}
	go func() {
		sc := bufio.NewScanner(out)
		if sc.Scan() {
			slotMu.Lock()
			slotID[e.slot()] = strings.TrimSpace(sc.Text())
			slotMu.Unlock()
		}
		for sc.Scan() {
			if k := strings.TrimSpace(sc.Text()); k != "" {
				acts <- k
			}
		}
		cmd.Wait()
	}()
}

func focused() window {
	out, err := exec.Command("niri", "msg", "-j", "focused-window").Output()
	if err != nil {
		return window{}
	}
	var w struct {
		AppID string `json:"app_id"`
		Title string `json:"title"`
	}
	json.Unmarshal(out, &w)
	return window{App: w.AppID, Title: w.Title}
}

func spawn(dir, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Println("spawn", name, err)
		return
	}
	go cmd.Wait()
}

// launchWork opens the problem in the browser and nvim where competitest
// expects it: Competitive Companion's click then creates the file with samples.
func launchWork(t *Task) {
	spawn(workDir(), "xdg-open", t.URL)
	p := solutionPath(t)
	dir := filepath.Join(workDir(), "codeforces")
	args := []string{"--title", "edge · " + t.Title, "--working-directory", dir, "-e", "nvim"}
	if t.Source() == "lc" {
		dir = filepath.Dir(p)
		args = []string{"--title", "edge · " + t.Title, "--working-directory", dir, "-e", "nvim", p}
	} else if rel, err := filepath.Rel(dir, p); err == nil && exists(p) {
		args = append(args, rel) // work in progress
	}
	spawn(dir, "alacritty", args...)
}

// launchClaude opens Claude in ~/CP with the prompt already sent.
func launchClaude(prompt string) {
	spawn(workDir(), "alacritty", "--title", "edge · claude", "--working-directory", workDir(), "-e", "claude", prompt)
}
