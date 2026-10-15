package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// solutionPath is where the solution lives in the ~/CP layout. Codeforces
// files are created by competitest when Competitive Companion sends the
// problem (with its samples); LeetCode files are created here.
func solutionPath(t *Task) string {
	if t.Source() == "cf" {
		id := strings.TrimPrefix(t.ID, "cf:")
		c := contestOf(t.ID)
		return filepath.Join(workDir(), "codeforces", c, id[len(c):]+".cpp")
	}
	return filepath.Join(workDir(), "leetcode", strings.TrimPrefix(t.ID, "lc:")+".cpp")
}

// archive moves an old solution (a cold re-solve, or an earlier try) into
// edge's archive when the task is assigned: nothing to peek at, and
// competitest doesn't stop to ask about overwriting.
func archive(t *Task) {
	p := solutionPath(t)
	st, err := os.Stat(p)
	if err != nil || !st.ModTime().Before(t.Assigned) {
		return
	}
	dir := filepath.Join(dataDir(), "archive")
	os.MkdirAll(dir, 0o755)
	base := fmt.Sprintf("%s-%d", strings.ReplaceAll(t.ID, ":", "-"), st.ModTime().Unix())
	dst := filepath.Join(dir, base+".cpp")
	for i := 2; exists(dst); i++ {
		dst = filepath.Join(dir, fmt.Sprintf("%s-%d.cpp", base, i))
	}
	if err := os.Rename(p, dst); err != nil {
		fmt.Fprintln(os.Stderr, "edge: couldn't archive the old solution:", err)
	}
}

// scaffold writes a LeetCode starter file if there isn't one. Codeforces
// files come from competitest. Network: call it outside the lock.
func scaffold(t *Task) error {
	p := solutionPath(t)
	if t.Source() != "lc" || exists(p) {
		return nil
	}
	slug := strings.TrimPrefix(t.ID, "lc:")
	starter, err := lcStarter(slug)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("/**\n * author: %s\n * created: %s\n * %s\n **/\n#include <bits/stdc++.h>\n\nusing namespace std;\n\n%s\n",
		cfg().LCUser, now().Format("Monday 02 January 2006 03:04:05 PM"), t.URL, starter)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}

func lcStarter(slug string) (string, error) {
	var r struct {
		Q struct {
			Snippets []struct {
				Lang string `json:"langSlug"`
				Code string `json:"code"`
			} `json:"codeSnippets"`
		} `json:"question"`
	}
	if err := lcQuery(`query($s:String!){question(titleSlug:$s){codeSnippets{langSlug code}}}`,
		map[string]any{"s": slug}, &r); err != nil {
		return "", err
	}
	for _, sn := range r.Q.Snippets {
		if sn.Lang == "cpp" {
			return sn.Code, nil
		}
	}
	return "", fmt.Errorf("no C++ starter for %s", slug)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
