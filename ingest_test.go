package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildOpencodeDB creates a minimal opencode-shaped sqlite at path.
func buildOpencodeDB(t *testing.T, path string, messages []struct {
	id, sessionID string
	timeCreated   int64
	data          string
}) {
	t.Helper()
	src, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	stmts := []string{
		`CREATE TABLE project (id TEXT PRIMARY KEY, name TEXT, worktree TEXT)`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, directory TEXT)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INT, time_updated INT, data TEXT)`,
		`INSERT INTO project (id, name) VALUES ('p1', 'coolproj')`,
		`INSERT INTO session (id, project_id, directory) VALUES ('s1', 'p1', '/some/dir')`,
		`INSERT INTO session (id, project_id, directory) VALUES ('s2', NULL, '/tmp/whatever-xyz')`,
	}
	for _, s := range stmts {
		if _, err := src.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	for _, m := range messages {
		if _, err := src.Exec(`INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`,
			m.id, m.sessionID, m.timeCreated, m.data); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
}

func TestIngestOpencode(t *testing.T) {
	db := openTestDB(t)
	src := filepath.Join(t.TempDir(), "opencode.db")

	type msg = struct {
		id, sessionID string
		timeCreated   int64
		data          string
	}
	m1 := `{"role":"assistant","path":{"cwd":"/some/dir","root":"/some/dir"},"cost":0.02,` +
		`"tokens":{"input":10,"output":20,"reasoning":5,"cache":{"read":100,"write":3}},` +
		`"modelID":"MiniMax-M3","providerID":"minimax",` +
		`"time":{"created":1788115499779,"completed":1788115566028}}`
	m2 := `{"role":"assistant","path":{"cwd":"/tmp/whatever-xyz"},"cost":0.1,` +
		`"tokens":{"input":1,"output":2,"cache":{"read":0,"write":0}},` +
		`"modelID":"gpt-x","providerID":"",` +
		`"time":{"created":1788115600000,"completed":1788115602000}}`
	m3 := `{"role":"assistant","error":{"name":"boom"},"modelID":"x","providerID":"y",` +
		`"time":{"created":1788115700000,"completed":1788115701000}}`
	m4 := `{"role":"user","time":{"created":1788115800000}}`

	buildOpencodeDB(t, src, []msg{
		{"msg1", "s1", 1, m1},
		{"msg2", "s2", 2, m2},
		{"msg3", "s1", 3, m3},
		{"msg4", "s1", 4, m4},
	})

	n, err := ingestOpencode(db, src, time.Now())
	if err != nil {
		t.Fatalf("ingestOpencode: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 inserted, got %d", n)
	}

	var modelo, proyecto, ts string
	var din sql.NullFloat64
	var tin, tout, tcache int64
	var costo float64
	if err := db.QueryRow(`SELECT modelo, proyecto, ts, duracion_s, tokens_in, tokens_out, tokens_cache, costo_usd
		FROM responses WHERE external_id='msg1'`).Scan(&modelo, &proyecto, &ts, &din, &tin, &tout, &tcache, &costo); err != nil {
		t.Fatalf("scan msg1: %v", err)
	}
	if modelo != "minimax/MiniMax-M3" {
		t.Errorf("modelo = %q", modelo)
	}
	if proyecto != "coolproj" {
		t.Errorf("proyecto = %q", proyecto)
	}
	if !din.Valid || din.Float64 < 66.2 || din.Float64 > 66.3 {
		t.Errorf("duracion_s = %v, want ~66.25", din)
	}
	if tin != 10 || tout != 20 || tcache != 103 {
		t.Errorf("tokens = %d/%d/%d, want 10/20/103", tin, tout, tcache)
	}
	if costo != 0.02 {
		t.Errorf("costo = %v", costo)
	}
	if len(ts) != len(tsLayout) {
		t.Errorf("ts not in tsLayout: %q", ts)
	}

	var modelo2, proyecto2 string
	if err := db.QueryRow(`SELECT modelo, proyecto FROM responses WHERE external_id='msg2'`).
		Scan(&modelo2, &proyecto2); err != nil {
		t.Fatalf("scan msg2: %v", err)
	}
	if modelo2 != "gpt-x" {
		t.Errorf("msg2 modelo = %q, want gpt-x", modelo2)
	}
	if proyecto2 != "whatever-xyz" {
		t.Errorf("msg2 proyecto = %q, want whatever-xyz", proyecto2)
	}

	// Re-run: watermark + dedup => nothing new.
	n, err = ingestOpencode(db, src, time.Now())
	if err != nil || n != 0 {
		t.Fatalf("re-run: n=%d err=%v", n, err)
	}

	// Missing srcPath.
	n, err = ingestOpencode(db, filepath.Join(t.TempDir(), "nope.db"), time.Now())
	if err != nil || n != 0 {
		t.Fatalf("missing src: n=%d err=%v", n, err)
	}
}

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
