package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerNeverClosesHumanHours(t *testing.T) {
	db := openTestDB(t)
	cfg := loadConfig(t.TempDir())
	cfg.IngestEnabled = false
	_, err := db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado) VALUES ('human work','project','2026-08-30T09:00:00','abierto')`)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := getEntry(db, 1)
	for _, elapsed := range []time.Duration{11 * time.Minute, 3 * time.Hour, 48 * time.Hour} {
		now := time.Date(2026, 8, 30, 9, 0, 0, 0, time.Local).Add(elapsed)
		if err := runWorkerOnce(db, cfg, now); err != nil {
			t.Fatal(err)
		}
		after, _ := getEntry(db, 1)
		if after != before {
			t.Fatalf("worker changed human entry after %s: %+v", elapsed, after)
		}
	}
}

func TestWorkerStillImportsMetricsWithoutClosingEntries(t *testing.T) {
	db := openTestDB(t)
	cfg := loadConfig(t.TempDir())
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg.ClaudeProjectsDir = filepath.Dir(dir)
	line := `{"type":"assistant","timestamp":"2026-08-30T20:00:00Z","cwd":"/not/a/project","message":{"id":"worker-message","model":"test","usage":{"input_tokens":1}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "chat.jsonl"), []byte(line), 0644); err != nil {
		t.Fatal(err)
	}
	id, _, _ := insertOpen(db, "human task", "project")
	if err := runWorkerOnce(db, cfg, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM responses").Scan(&count); err != nil || count != 1 {
		t.Fatalf("metrics count=%d error=%v", count, err)
	}
	entry, _ := getEntry(db, id)
	if entry.Estado != "abierto" {
		t.Fatal("ingestion closed human entry")
	}
}
