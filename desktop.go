package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"syscall"
)

var (
	slotMu   gosync.Mutex
	slotID   = map[string]string{}    // slot → notification id on screen, for replacing
	slotWait = map[string]*exec.Cmd{} // slot → the notify-send waiting on its buttons
)

// notify shows e via notify-send; a clicked button's key goes to acts.
// Replacing a slot's notification stops the old waiter first, so one click
// can't come back twice. False means it couldn't be shown.
func notify(e event, acts chan<- string) bool {
	args := []string{"-a", "edge", "-p"}
	if e.Urgent {
		args = append(args, "-u", "critical")
	}
	slotMu.Lock()
	if id := slotID[e.slot()]; id != "" {
		args = append(args, "-r", id)
	}
	if old := slotWait[e.slot()]; old != nil && old.Process != nil {
		old.Process.Kill()
		delete(slotWait, e.slot())
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
		return false
	}
	if err := cmd.Start(); err != nil {
		log.Println("notify:", err)
		return false
	}
	if len(e.Actions) > 0 {
		slotMu.Lock()
		slotWait[e.slot()] = cmd
		slotMu.Unlock()
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
		slotMu.Lock()
		if slotWait[e.slot()] == cmd {
			delete(slotWait, e.slot())
		}
		slotMu.Unlock()
	}()
	return true
}

func focused() window {
	out, err := exec.Command("niri", "msg", "-j", "focused-window").Output()
	if err != nil {
		return window{}
	}
	var w struct {
		AppID string `json:"app_id"`
		Title string `json:"title"`
		PID   int    `json:"pid"`
	}
	json.Unmarshal(out, &w)
	return window{App: w.AppID, Title: w.Title, PID: w.PID}
}

// procCwds is the working directories of every process under pid (not pid
// itself: a terminal's own cwd is wherever it was launched from). Swapped
// in tests.
var procCwds = func(pid int) []string {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	kids := map[int][]int{}
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...: comm may hold spaces, so split after ')'
		st := string(b)
		f := strings.Fields(st[strings.LastIndexByte(st, ')')+1:])
		if len(f) < 2 {
			continue
		}
		pp, _ := strconv.Atoi(f[1])
		kids[pp] = append(kids[pp], p)
	}
	var cwds []string
	queue := slices.Clone(kids[pid])
	for len(queue) > 0 && len(cwds) < 256 {
		p := queue[0]
		queue = queue[1:]
		if d, err := os.Readlink("/proc/" + strconv.Itoa(p) + "/cwd"); err == nil {
			cwds = append(cwds, d)
		}
		queue = append(queue, kids[p]...)
	}
	return cwds
}

// spawn starts a program outside edge's service, through niri when it's
// there: restarting or updating edge must not take the user's editor or
// Claude window with it. (The unit's KillMode=process is the second guard.)
func spawn(dir, name string, args ...string) {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("niri"); err == nil && os.Getenv("NIRI_SOCKET") != "" {
		cmd = exec.Command("niri", append([]string{"msg", "action", "spawn", "--", name}, args...)...)
	} else {
		cmd = exec.Command(name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		log.Println("spawn", name, err)
		return
	}
	go cmd.Wait()
}

// launchWork opens the problem in the browser and the editor where
// competitest expects it: Competitive Companion's click then creates the
// file with samples.
func launchWork(t *Task) {
	c := cfg()
	spawn(workDir(), c.Browser, t.URL)
	p := solutionPath(t)
	dir := filepath.Join(workDir(), "codeforces")
	args := []string{"--title", "edge · " + t.Title, "--working-directory", dir, "-e", c.Editor}
	if t.Source() == "lc" {
		dir = filepath.Dir(p)
		args = []string{"--title", "edge · " + t.Title, "--working-directory", dir, "-e", c.Editor, p}
	} else if rel, err := filepath.Rel(dir, p); err == nil && exists(p) {
		args = append(args, rel) // work in progress
	}
	spawn(dir, c.Terminal, args...)
}

// launchClaude opens Claude in the workspace with the prompt already sent.
func launchClaude(prompt string) {
	spawn(workDir(), cfg().Terminal, "--title", "edge · claude", "--working-directory", workDir(), "-e", "claude", prompt)
}
