package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// deriveProyecto guesses a project name for a working directory. It walks up
// looking for a .git directory. When found it prefers the origin remote's repo
// name, falling back to the basename of the git root. With no .git anywhere it
// returns the basename of dir.
func deriveProyecto(dir string) string {
	if dir == "" {
		return ""
	}
	root := gitRoot(dir)
	if root == "" {
		return filepath.Base(dir)
	}
	if name := originRepoName(filepath.Join(root, ".git", "config")); name != "" {
		return name
	}
	return filepath.Base(root)
}

// gitRoot walks up from dir until it finds a directory containing .git.
func gitRoot(dir string) string {
	cur := dir
	for {
		if fi, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			_ = fi
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// originRepoName parses a git config file and returns the last path segment of
// the [remote "origin"] url, without a trailing ".git". Returns "" if not found.
func originRepoName(configPath string) string {
	f, err := os.Open(configPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	inOrigin := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.EqualFold(line, `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		if !strings.HasPrefix(line, "url") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		return repoNameFromURL(strings.TrimSpace(line[eq+1:]))
	}
	return ""
}

// repoNameFromURL turns a git remote URL into its bare repo name.
func repoNameFromURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, "/")
	url = strings.TrimSuffix(url, ".git")
	// Split on both ':' (scp-style) and '/'.
	url = strings.ReplaceAll(url, ":", "/")
	seg := strings.Split(url, "/")
	if len(seg) == 0 {
		return ""
	}
	return seg[len(seg)-1]
}
