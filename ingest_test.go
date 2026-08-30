package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveProyecto(t *testing.T) {
	// (a) plain dir -> basename
	plain := filepath.Join(t.TempDir(), "some-folder")
	if err := os.MkdirAll(plain, 0755); err != nil {
		t.Fatal(err)
	}
	if got := deriveProyecto(plain); got != "some-folder" {
		t.Errorf("plain dir: got %q, want some-folder", got)
	}

	// (b) git root with origin remote -> repo name from url
	root := filepath.Join(t.TempDir(), "checkout")
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = git@github.com:foo/my-repo.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	if got := deriveProyecto(root); got != "my-repo" {
		t.Errorf("git root: got %q, want my-repo", got)
	}

	// (c) nested subdir under the git root -> still my-repo
	nested := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if got := deriveProyecto(nested); got != "my-repo" {
		t.Errorf("nested subdir: got %q, want my-repo", got)
	}

	// (d) git root without a usable remote -> basename of root
	root2 := filepath.Join(t.TempDir(), "barerepo")
	if err := os.MkdirAll(filepath.Join(root2, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root2, ".git", "config"), []byte("[core]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := deriveProyecto(root2); got != "barerepo" {
		t.Errorf("no remote: got %q, want barerepo", got)
	}
}
