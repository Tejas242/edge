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

// prepare gets the file ready for a fresh attempt. An old solution (a cold
// re-solve, or an earlier try) is moved into edge's archive first: nothing to
// peek at, and competitest doesn't stop to ask about overwriting.
func prepare(t *Task) (string, error) {
	p := solutionPath(t)
	if st, err := os.Stat(p); err == nil && st.ModTime().Before(t.Assigned) {
		arch := filepath.Join(dataDir(), "archive")
		os.MkdirAll(arch, 0o755)
		dst := filepath.Join(arch, fmt.Sprintf("%s-%d.cpp", strings.ReplaceAll(t.ID, ":", "-"), st.ModTime().Unix()))
		if err := os.Rename(p, dst); err != nil {
			return "", err
		}
	}
	if t.Source() == "cf" {
		return p, nil
	}
	if _, err := os.Stat(p); err == nil {
		return p, nil // work in progress
	}
	slug := strings.TrimPrefix(t.ID, "lc:")
	starter, err := lcStarter(slug)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("/**\n * author: screenager\n * created: %s\n * %s\n **/\n#include <bits/stdc++.h>\n\nusing namespace std;\n\n%s\n",
		now().Format("Monday 02 January 2006 03:04:05 PM"), t.URL, starter)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return p, os.WriteFile(p, []byte(body), 0o644)
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
