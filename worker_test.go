package main

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestComputeIdleCloses(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.Local)
	threshold := 10 * time.Minute

	open := []Entry{
		{ID: 1, Proyecto: "a", Inicio: "2026-08-30T11:00:00.000000"}, // stale via Inicio fallback
		{ID: 2, Proyecto: "b", Inicio: "2026-08-30T08:00:00.000000"}, // fresh via map
		{ID: 3, Proyecto: "c", Inicio: "2026-08-30T11:58:00.000000"}, // fresh via Inicio fallback
	}
	lastActivity := map[int64]time.Time{
		2: now.Add(-2 * time.Minute),
	}

	got := computeIdleCloses(open, lastActivity, now, threshold)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("computeIdleCloses = %v, want [1]", got)
	}
}

func TestLastActivityForUsesMaxSignal(t *testing.T) {
	db := openTestDB(t)

	res, err := db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado)
		VALUES ('task', 'proj-x', '2026-08-30T09:00:00.000000', 'abierto')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	e, _ := getEntry(db, id)

	// A response for the same proyecto, more recent than Inicio.
	_, err = insertResponse(db, Response{
		Ts: "2026-08-30T11:30:00.000000", Proyecto: "proj-x", Cliente: "opencode",
		Modelo: "m", Source: "opencode", ExternalID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := setMeta(db, "last_tool_call_at", "2026-08-30T11:45:00.000000"); err != nil {
		t.Fatal(err)
	}

	got := lastActivityFor(db, e)
	want, _ := parseTS("2026-08-30T11:45:00.000000")
	if !got.Equal(want) {
		t.Fatalf("lastActivityFor = %v, want %v (meta wins)", got, want)
	}
}

func TestRunWorkerOnceClosesIdleAndIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	cfg := loadConfig(t.TempDir())
	cfg.IngestEnabled = false
	cfg.IdleCloseMinutes = 10

	// Insert an open entry whose inicio is well in the past.
	_, err := db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado)
		VALUES ('old', 'p', '2026-08-30T09:00:00.000000', 'abierto')`)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.Local)
	if err := runWorkerOnce(db, cfg, now); err != nil {
		t.Fatalf("runWorkerOnce: %v", err)
	}

	var estado string
	if err := db.QueryRow(`SELECT estado FROM entries WHERE descripcion='old'`).Scan(&estado); err != nil {
		t.Fatal(err)
	}
	if estado != "cerrado" {
		t.Fatalf("entry estado = %q, want cerrado", estado)
	}

	// Second run: nothing to do, no error.
	if err := runWorkerOnce(db, cfg, now.Add(time.Hour)); err != nil {
		t.Fatalf("second runWorkerOnce: %v", err)
	}
	var openCount int
	db.QueryRow(`SELECT COUNT(*) FROM entries WHERE estado='abierto'`).Scan(&openCount)
	if openCount != 0 {
		t.Fatalf("open entries after second run = %d, want 0", openCount)
	}
}

func TestWithActivityUpdatesMeta(t *testing.T) {
	db := newTestDB(t)

	base := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	wrapped := withActivity(db, base)

	if _, err := wrapped(context.Background(), callReq(nil)); err != nil {
		t.Fatalf("wrapped handler: %v", err)
	}

	v, err := getMeta(db, "last_tool_call_at")
	if err != nil {
		t.Fatal(err)
	}
	if v == "" {
		t.Fatal("last_tool_call_at not set after wrapped call")
	}
	if _, err := parseTS(v); err != nil {
		t.Fatalf("last_tool_call_at not in tsLayout: %q (%v)", v, err)
	}
}
