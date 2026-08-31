package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseProjectFileInlineAlias(t *testing.T) {
	content := []byte("---\nproyecto: demo\nalias: [api-demo, web-demo]\n---\nnotes ignored\n")
	pf := parseProjectFile(content)
	if pf.Proyecto != "demo" {
		t.Errorf("Proyecto = %q, want demo", pf.Proyecto)
	}
	if len(pf.Alias) != 2 || pf.Alias[0] != "api-demo" || pf.Alias[1] != "web-demo" {
		t.Errorf("Alias = %v, want [api-demo web-demo]", pf.Alias)
	}
}

func TestParseProjectFileBlockAlias(t *testing.T) {
	content := []byte("---\nproyecto: demo\nalias:\n  - api-demo\n  - web-demo\n---\n")
	pf := parseProjectFile(content)
	if pf.Proyecto != "demo" {
		t.Errorf("Proyecto = %q, want demo", pf.Proyecto)
	}
	if len(pf.Alias) != 2 || pf.Alias[0] != "api-demo" || pf.Alias[1] != "web-demo" {
		t.Errorf("Alias = %v, want [api-demo web-demo]", pf.Alias)
	}
}

func TestParseProjectFileNoFrontmatter(t *testing.T) {
	pf := parseProjectFile([]byte("just some notes\nno frontmatter here\n"))
	if pf.Proyecto != "" || len(pf.Alias) != 0 {
		t.Errorf("expected zero value, got %+v", pf)
	}
}

func TestParseProjectFileProyectoMissing(t *testing.T) {
	pf := parseProjectFile([]byte("---\nalias: [a, b]\n---\n"))
	if pf.Proyecto != "" {
		t.Errorf("Proyecto = %q, want empty", pf.Proyecto)
	}
	if len(pf.Alias) != 2 {
		t.Errorf("Alias = %v, want 2 entries", pf.Alias)
	}
}

func TestFindProjectFileWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hh-tracker.md"),
		[]byte("---\nproyecto: demo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	pf, ok := findProjectFile(nested)
	if !ok {
		t.Fatal("findProjectFile: ok=false, want true")
	}
	if pf.Proyecto != "demo" {
		t.Errorf("Proyecto = %q, want demo", pf.Proyecto)
	}
	if pf.Path != filepath.Join(root, ".hh-tracker.md") {
		t.Errorf("Path = %q", pf.Path)
	}
}

func TestFindProjectFileNotFound(t *testing.T) {
	dir := t.TempDir()
	if _, ok := findProjectFile(dir); ok {
		t.Error("findProjectFile: ok=true, want false")
	}
}

func TestResolveProyectoPrefersFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hh-tracker.md"),
		[]byte("---\nproyecto: canon\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolveProyecto(root); got != "canon" {
		t.Errorf("resolveProyecto = %q, want canon", got)
	}
}

func TestResolveProyectoFallsBackToDerive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "some-folder")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// No .hh-tracker.md, no git => deriveProyecto returns the basename.
	if got := resolveProyecto(dir); got != "some-folder" {
		t.Errorf("resolveProyecto = %q, want some-folder", got)
	}
}

// setupWorktree makes a committed git repo at <base>/<name> plus a linked
// worktree at <base>/<name>-wt, returning both paths.
func setupWorktree(t *testing.T, name string) (mainRoot, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	mainRoot = filepath.Join(base, name)
	if err := os.MkdirAll(mainRoot, 0755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, mainRoot, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(mainRoot, "seed.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, mainRoot, "add", ".")
	gitCmd(t, mainRoot, "commit", "-m", "seed")
	worktree = filepath.Join(base, name+"-wt")
	gitCmd(t, mainRoot, "worktree", "add", worktree)
	return mainRoot, worktree
}

func TestResolveProyectoWorktreeUsesMainRoot(t *testing.T) {
	mainRoot, worktree := setupWorktree(t, "api-demo")

	// .hh-tracker.md exists ONLY at the main checkout root (left untracked so
	// `git worktree add` does not copy it into the worktree tree).
	if err := os.WriteFile(filepath.Join(mainRoot, ".hh-tracker.md"),
		[]byte("---\nproyecto: canon\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".hh-tracker.md")); !os.IsNotExist(err) {
		t.Fatalf("precondition: worktree should not have the file, err=%v", err)
	}

	if got := resolveProyecto(worktree); got != "canon" {
		t.Errorf("resolveProyecto(worktree) = %q, want canon", got)
	}
}

func TestResolveProyectoFileInsideWorktree(t *testing.T) {
	mainRoot, worktree := setupWorktree(t, "web-demo")
	// Committed file: present in both the main checkout and the worktree tree.
	if err := os.WriteFile(filepath.Join(mainRoot, ".hh-tracker.md"),
		[]byte("---\nproyecto: inside\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, mainRoot, "add", ".hh-tracker.md")
	gitCmd(t, mainRoot, "commit", "-m", "add project file")
	gitCmd(t, worktree, "merge", "main")

	if _, err := os.Stat(filepath.Join(worktree, ".hh-tracker.md")); err != nil {
		t.Fatalf("precondition: worktree should have the committed file: %v", err)
	}
	if got := resolveProyecto(worktree); got != "inside" {
		t.Errorf("resolveProyecto(worktree) = %q, want inside", got)
	}
}

func TestResolveProyectoWorktreeNoFileFallsBack(t *testing.T) {
	_, worktree := setupWorktree(t, "no-file-repo")
	// deriveProyecto has no remote -> basename of the worktree toplevel.
	if got := resolveProyecto(worktree); got != filepath.Base(worktree) {
		t.Errorf("resolveProyecto(worktree) = %q, want %q", got, filepath.Base(worktree))
	}
}

func TestMainCheckoutRootFromWorktree(t *testing.T) {
	mainRoot, worktree := setupWorktree(t, "demo-main")

	got, err := mainCheckoutRoot(worktree)
	if err != nil {
		t.Fatalf("mainCheckoutRoot: %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(mainRoot)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("mainCheckoutRoot(worktree) = %q, want %q", gotResolved, wantResolved)
	}

	// Not a git dir -> error.
	if _, err := mainCheckoutRoot(t.TempDir()); err == nil {
		t.Error("mainCheckoutRoot on non-git dir: expected error")
	}
}
