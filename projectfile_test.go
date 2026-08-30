package main

import (
	"os"
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
