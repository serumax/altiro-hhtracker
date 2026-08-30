package main

import (
	"database/sql"
	"fmt"
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

func TestIngestClaude(t *testing.T) {
	db := openTestDB(t)
	projects := t.TempDir()
	slug := filepath.Join(projects, "-Users-me-proj")
	if err := os.MkdirAll(slug, 0755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(slug, "sess.jsonl")

	lines := []string{
		`{"type":"user","uuid":"u0","timestamp":"2026-08-30T18:59:14.881Z","cwd":"/Users/me/proj"}`,
		// Two copies of the same assistant message: different line uuid, SAME message.id.
		`{"type":"assistant","uuid":"a1a","timestamp":"2026-08-30T18:59:19.881Z","cwd":"/Users/me/proj","message":{"id":"msg_01AA","model":"claude-sonnet-5","usage":{"input_tokens":2,"output_tokens":84,"cache_creation_input_tokens":9261,"cache_read_input_tokens":138343,"output_tokens_details":{"thinking_tokens":8}}}}`,
		`{"type":"assistant","uuid":"a1b","timestamp":"2026-08-30T18:59:25.881Z","cwd":"/Users/me/proj","message":{"id":"msg_01AA","model":"claude-sonnet-5","usage":{"input_tokens":2,"output_tokens":84,"cache_creation_input_tokens":9261,"cache_read_input_tokens":138343}}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-08-30T19:00:00.000Z","cwd":"/Users/me/proj","message":{"id":"msg_01BB","model":"claude-sonnet-5","usage":{"input_tokens":5,"output_tokens":5}}}`,
		`{"type":"assistant","isSidechain":true,"uuid":"a3","timestamp":"2026-08-30T19:01:00.000Z","cwd":"/Users/me/proj","message":{"id":"msg_01SIDE","model":"claude-sonnet-5","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
	if err := os.WriteFile(f, []byte(joinLines(lines)), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := ingestClaude(db, projects, time.Now())
	if err != nil {
		t.Fatalf("ingestClaude: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 inserted, got %d", n)
	}

	var tin, tout, tcache int64
	var din sql.NullFloat64
	var cliente, source string
	var costo float64
	if err := db.QueryRow(`SELECT tokens_in, tokens_out, tokens_cache, duracion_s, cliente, source, costo_usd
		FROM responses WHERE external_id='msg_01AA'`).Scan(&tin, &tout, &tcache, &din, &cliente, &source, &costo); err != nil {
		t.Fatalf("scan msg_01AA: %v", err)
	}
	if tin != 2 || tout != 84 || tcache != 9261+138343 {
		t.Errorf("msg_01AA tokens = in %d out %d cache %d", tin, tout, tcache)
	}
	if !din.Valid || din.Float64 < 4.9 || din.Float64 > 5.1 {
		t.Errorf("msg_01AA duracion_s = %v, want ~5 (first occurrence wins)", din)
	}
	if cliente != "claude-code" || source != "claude-code" || costo != 0 {
		t.Errorf("msg_01AA meta = %q/%q/%v", cliente, source, costo)
	}

	// The two copies collapse to one row.
	var rowsAA int
	db.QueryRow(`SELECT COUNT(*) FROM responses WHERE external_id='msg_01AA'`).Scan(&rowsAA)
	if rowsAA != 1 {
		t.Fatalf("msg_01AA row count = %d, want 1", rowsAA)
	}

	// Re-run: nothing new.
	n, err = ingestClaude(db, projects, time.Now())
	if err != nil || n != 0 {
		t.Fatalf("re-run: n=%d err=%v", n, err)
	}

	// Append one more assistant line, re-run -> exactly 1 new.
	extra := "\n" + `{"type":"assistant","uuid":"a9","timestamp":"2026-08-30T19:05:00.000Z","cwd":"/Users/me/proj","message":{"id":"msg_01CC","model":"claude-sonnet-5","usage":{"input_tokens":3,"output_tokens":3}}}` + "\n"
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(extra)
	fh.Close()

	n, err = ingestClaude(db, projects, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("after append: n=%d err=%v", n, err)
	}

	// Missing dir -> 0, nil.
	n, err = ingestClaude(db, filepath.Join(t.TempDir(), "nope"), time.Now())
	if err != nil || n != 0 {
		t.Fatalf("missing dir: n=%d err=%v", n, err)
	}
}

func TestIngestClaudeDedupByMessageID(t *testing.T) {
	db := openTestDB(t)
	projects := t.TempDir()
	slug := filepath.Join(projects, "-Users-me-proj")
	if err := os.MkdirAll(slug, 0755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(slug, "sess.jsonl")

	dup := `{"type":"assistant","uuid":"%s","timestamp":"2026-08-30T20:00:0%d.000Z","cwd":"/Users/me/proj","message":{"id":"msg_01SAME","model":"claude-sonnet-5","usage":{"input_tokens":7,"output_tokens":11,"cache_creation_input_tokens":100,"cache_read_input_tokens":200}}}`
	lines := []string{
		fmt.Sprintf(dup, "x1", 1),
		fmt.Sprintf(dup, "x2", 2),
		fmt.Sprintf(dup, "x3", 3),
		// No message.id -> skipped entirely (no uuid fallback).
		`{"type":"assistant","uuid":"noid","timestamp":"2026-08-30T20:00:09.000Z","cwd":"/Users/me/proj","message":{"model":"claude-sonnet-5","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
	if err := os.WriteFile(f, []byte(joinLines(lines)), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := ingestClaude(db, projects, time.Now())
	if err != nil {
		t.Fatalf("ingestClaude: %v", err)
	}
	if n != 1 {
		t.Fatalf("3 copies of one message.id + 1 id-less line: expected 1 inserted, got %d", n)
	}

	var count int
	var tin, tout, tcache int64
	if err := db.QueryRow(`SELECT COUNT(*), SUM(tokens_in), SUM(tokens_out), SUM(tokens_cache) FROM responses`).
		Scan(&count, &tin, &tout, &tcache); err != nil {
		t.Fatal(err)
	}
	if count != 1 || tin != 7 || tout != 11 || tcache != 300 {
		t.Fatalf("tokens counted more than once: count=%d in=%d out=%d cache=%d", count, tin, tout, tcache)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
