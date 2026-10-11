package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scaffold creates the solution file in the ~/CP layout. Re-solves get a
// fresh file next to the old one, so there's nothing to copy from.
func scaffold(t *Task) (string, error) {
	var dir, name, body string
	stamp := now().Format("Monday 02 January 2006 03:04:05 PM")
	suffix := ""
	if t.Kind != "new" {
		suffix = fmt.Sprintf(".cold%d", t.ProbeStage)
	}
	if t.Source() == "cf" {
		id := strings.TrimPrefix(t.ID, "cf:")
		c := contestOf(t.ID)
		dir, name = filepath.Join(workDir(), "codeforces", c), id[len(c):]+suffix+".cpp"
		tpl, err := os.ReadFile(filepath.Join(workDir(), "lib", "template.cpp"))
		if err != nil {
			return "", err
		}
		body = strings.Replace(string(tpl), "$(DATE)", stamp, 1)
	} else {
		slug := strings.TrimPrefix(t.ID, "lc:")
		dir, name = filepath.Join(workDir(), "leetcode"), slug+suffix+".cpp"
		starter, err := lcStarter(slug)
		if err != nil {
			return "", err
		}
		body = fmt.Sprintf("/**\n * author: screenager\n * created: %s\n * %s\n **/\n#include <bits/stdc++.h>\n\nusing namespace std;\n\n%s\n",
			stamp, t.URL, starter)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err == nil {
		return p, nil // never clobber work in progress
	} else if !errors.Is(err, os.ErrNotExist) {
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
