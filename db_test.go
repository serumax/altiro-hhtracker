package main

import (
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// openTestDB opens a fresh DB in a temp HOME for use in tests.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".hh-tracker", "hh.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedClosed inserts a closed row directly, bypassing handler logic.
func seedClosed(t *testing.T, db *sql.DB, desc, proj, inicio, fin string, durHH float64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, fin, duracion_hh, estado)
		VALUES (?, ?, ?, ?, ?, 'cerrado')`, desc, proj, inicio, fin, durHH)
	if err != nil {
		t.Fatalf("seedClosed: %v", err)
	}
}

// ---- openDB + schema ------------------------------------------------------

func TestOpenDBAutoMkdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The .hh-tracker subdir does not exist yet.
	dbPath := filepath.Join(home, ".hh-tracker", "hh.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(filepath.Dir(dbPath)); os.IsNotExist(err) {
		t.Error("openDB: expected .hh-tracker dir to be created")
	}

	// Verify schema by inserting.
	_, err = db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado)
		VALUES ('test', 'general', '2026-08-01T09:00:00.000000', 'abierto')`)
	if err != nil {
		t.Fatalf("schema check insert: %v", err)
	}
}

func TestOpenDBSchemaIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".hh-tracker", "hh.db")

	db1, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("first openDB: %v", err)
	}
	db1.Close()

	// Second open must succeed (CREATE TABLE IF NOT EXISTS).
	db2, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("second openDB (idempotent): %v", err)
	}
	db2.Close()
}

// ---- migration (4 combos) -------------------------------------------------

func TestMigrateDBSrcPresentDstAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srcDir := filepath.Join(home, ".claude", "mcp-servers", "hh-tracker")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(srcDir, "hh.db")
	if err := os.WriteFile(srcPath, []byte("fake-db-content"), 0644); err != nil {
		t.Fatal(err)
	}

	dstPath := filepath.Join(home, ".hh-tracker", "hh.db")
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		t.Fatal(err)
	}

	if err := migrateDB(srcPath, dstPath); err != nil {
		t.Fatalf("migrateDB: %v", err)
	}
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		t.Error("migrateDB: dst not created")
	}
	// Copy, not move: source must still be there.
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		t.Error("migrateDB: src removed (must be copy, not move)")
	}
}

func TestMigrateDBSrcAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srcPath := filepath.Join(home, "nonexistent", "hh.db")
	dstPath := filepath.Join(home, ".hh-tracker", "hh.db")

	if err := migrateDB(srcPath, dstPath); err != nil {
		t.Fatalf("migrateDB with absent src: got error %v", err)
	}
	if _, err := os.Stat(dstPath); !os.IsNotExist(err) {
		t.Error("migrateDB: dst should not exist when src is absent")
	}
}

func TestMigrateDBDstAlreadyPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srcDir := filepath.Join(home, ".claude", "mcp-servers", "hh-tracker")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(srcDir, "hh.db")
	if err := os.WriteFile(srcPath, []byte("src-content"), 0644); err != nil {
		t.Fatal(err)
	}

	dstPath := filepath.Join(home, ".hh-tracker", "hh.db")
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		t.Fatal(err)
	}
	existingContent := []byte("existing-dst-content")
	if err := os.WriteFile(dstPath, existingContent, 0644); err != nil {
		t.Fatal(err)
	}

	if err := migrateDB(srcPath, dstPath); err != nil {
		t.Fatalf("migrateDB: %v", err)
	}

	// Must NOT overwrite existing destination.
	got, _ := os.ReadFile(dstPath)
	if string(got) != string(existingContent) {
		t.Errorf("migrateDB: dst overwritten; got %q, want %q", got, existingContent)
	}
}

func TestMigrateDBBothAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcPath := filepath.Join(home, "no-src", "hh.db")
	dstPath := filepath.Join(home, ".hh-tracker", "hh.db")

	if err := migrateDB(srcPath, dstPath); err != nil {
		t.Fatalf("migrateDB both absent: got error %v", err)
	}
}

// ---- new tables (responses / ingest_state / meta) ------------------------

func TestOpenDBNewTables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".hh-tracker", "hh.db")

	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO responses
		(ts, proyecto, cliente, modelo, tokens_cache_write, tokens_cache_read, source, external_id)
		VALUES ('2026-08-01T09:00:00.000000', 'p', 'claude-code', 'claude-sonnet-5', 12, 34, 'claude-code', 'abc')`); err != nil {
		t.Fatalf("insert responses: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ingest_state (source, watermark) VALUES ('opencode', '123')`); err != nil {
		t.Fatalf("insert ingest_state: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('k', 'v')`); err != nil {
		t.Fatalf("insert meta: %v", err)
	}
	db.Close()

	// Second open on same path stays green (idempotent) and data survives.
	db2, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("second openDB: %v", err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM responses`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("responses row missing after reopen: n=%d err=%v", n, err)
	}
}

// ---- insertOpen -----------------------------------------------------------

func TestInsertOpen(t *testing.T) {
	db := openTestDB(t)

	id, inicio, err := insertOpen(db, "fix auth", "my-project")
	if err != nil {
		t.Fatalf("insertOpen: %v", err)
	}
	if id <= 0 {
		t.Errorf("insertOpen: expected positive id, got %d", id)
	}
	if inicio == "" {
		t.Error("insertOpen: expected non-empty inicio timestamp")
	}

	rows, err := listOpen(db)
	if err != nil {
		t.Fatalf("listOpen: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 open row, got %d", len(rows))
	}
	if rows[0].Descripcion != "fix auth" {
		t.Errorf("descripcion: got %q, want %q", rows[0].Descripcion, "fix auth")
	}
	if rows[0].Estado != "abierto" {
		t.Errorf("estado: got %q, want %q", rows[0].Estado, "abierto")
	}
}

// ---- closeEntry -----------------------------------------------------------

func TestCloseEntry(t *testing.T) {
	db := openTestDB(t)

	id, _, err := insertOpen(db, "task", "proj")
	if err != nil {
		t.Fatalf("insertOpen: %v", err)
	}

	entry, err := closeEntry(db, int(id), "done")
	if err != nil {
		t.Fatalf("closeEntry: %v", err)
	}
	if entry.Estado != "cerrado" {
		t.Errorf("closeEntry: estado = %q, want 'cerrado'", entry.Estado)
	}
	if !entry.DuracionHH.Valid {
		t.Error("closeEntry: duracion_hh should be valid (non-null)")
	}
	if entry.DuracionHH.Float64 < 0 {
		t.Errorf("closeEntry: duracion_hh = %v, expected >= 0", entry.DuracionHH.Float64)
	}
}

func TestCloseEntryNotFound(t *testing.T) {
	db := openTestDB(t)

	_, err := closeEntry(db, 9999, "")
	if err == nil {
		t.Error("closeEntry with missing id: expected error, got nil")
	}
}

// ---- listOpen -------------------------------------------------------------

func TestListOpenEmpty(t *testing.T) {
	db := openTestDB(t)

	rows, err := listOpen(db)
	if err != nil {
		t.Fatalf("listOpen: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 open rows, got %d", len(rows))
	}
}

func TestListOpenOrderDesc(t *testing.T) {
	db := openTestDB(t)

	// Insert two open entries with different inicio values.
	_, err := db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado) VALUES ('first', 'p', '2026-08-01T09:00:00.000000', 'abierto')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado) VALUES ('second', 'p', '2026-08-02T09:00:00.000000', 'abierto')`)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := listOpen(db)
	if err != nil {
		t.Fatalf("listOpen: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	// DESC: second (later) should come first.
	if rows[0].Descripcion != "second" {
		t.Errorf("listOpen: first row should be 'second' (DESC), got %q", rows[0].Descripcion)
	}
}

// ---- listClosed + listReporte ordering ------------------------------------

func TestListClosedOrderDesc(t *testing.T) {
	db := openTestDB(t)

	seedClosed(t, db, "earlier", "proj", "2026-08-01T09:00:00.000000", "2026-08-01T10:00:00.000000", 1.0)
	seedClosed(t, db, "later", "proj", "2026-08-02T09:00:00.000000", "2026-08-02T10:00:00.000000", 1.0)

	rows, err := listClosed(db, "", "", "")
	if err != nil {
		t.Fatalf("listClosed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Descripcion != "later" {
		t.Errorf("listClosed: first row should be 'later' (DESC), got %q", rows[0].Descripcion)
	}
}

func TestListReporteOrderAsc(t *testing.T) {
	db := openTestDB(t)

	seedClosed(t, db, "earlier", "proj", "2026-08-01T09:00:00.000000", "2026-08-01T10:00:00.000000", 1.0)
	seedClosed(t, db, "later", "proj", "2026-08-02T09:00:00.000000", "2026-08-02T10:00:00.000000", 1.0)

	rows, err := listReporte(db, "2026-08-01", "2026-08-02", "")
	if err != nil {
		t.Fatalf("listReporte: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	// ASC: earlier must come first.
	if rows[0].Descripcion != "earlier" {
		t.Errorf("listReporte: first row should be 'earlier' (ASC), got %q", rows[0].Descripcion)
	}
}

func TestListClosedDateFilter(t *testing.T) {
	db := openTestDB(t)

	seedClosed(t, db, "in-range", "proj", "2026-08-03T09:00:00.000000", "2026-08-03T10:00:00.000000", 1.0)
	seedClosed(t, db, "out-range", "proj", "2026-07-01T09:00:00.000000", "2026-07-01T10:00:00.000000", 1.0)

	rows, err := listClosed(db, "2026-08-01", "2026-08-31", "")
	if err != nil {
		t.Fatalf("listClosed with filter: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 filtered row, got %d", len(rows))
	}
	if rows[0].Descripcion != "in-range" {
		t.Errorf("listClosed filter: got %q, want 'in-range'", rows[0].Descripcion)
	}
}

// ---- insertManual ---------------------------------------------------------

func TestInsertManualModeA(t *testing.T) {
	db := openTestDB(t)

	entry, err := insertManual(db, "manual task", "proj",
		"2026-08-05T09:00:00.000000", "2026-08-05T10:30:00.000000", 0, "done")
	if err != nil {
		t.Fatalf("insertManual Mode A: %v", err)
	}
	if entry.Estado != "cerrado" {
		t.Errorf("insertManual: estado = %q, want 'cerrado'", entry.Estado)
	}
	if !entry.DuracionHH.Valid {
		t.Error("insertManual: duracion_hh should be valid")
	}
	if absDiff(entry.DuracionHH.Float64, 1.5) > 0.001 {
		t.Errorf("insertManual: duracion_hh = %.4f, want ~1.5", entry.DuracionHH.Float64)
	}
}

func TestInsertManualModeB(t *testing.T) {
	db := openTestDB(t)

	entry, err := insertManual(db, "manual B", "proj", "", "", 2.0, "")
	if err != nil {
		t.Fatalf("insertManual Mode B: %v", err)
	}
	if entry.Estado != "cerrado" {
		t.Errorf("insertManual B: estado = %q, want 'cerrado'", entry.Estado)
	}
	if !entry.DuracionHH.Valid || absDiff(entry.DuracionHH.Float64, 2.0) > 0.001 {
		t.Errorf("insertManual B: duracion_hh = %v, want 2.0", entry.DuracionHH)
	}
}

// ---- monthRange -----------------------------------------------------------

func TestMonthRangeJanMinus1(t *testing.T) {
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.Local)
	first, last := monthRange(now, -1)

	if first.Year() != 2025 || first.Month() != 12 || first.Day() != 1 {
		t.Errorf("monthRange Jan -1 first: got %v, want 2025-12-01", first)
	}
	if last.Year() != 2025 || last.Month() != 12 || last.Day() != 31 {
		t.Errorf("monthRange Jan -1 last: got %v, want 2025-12-31", last)
	}
}

func TestMonthRangeDecPlus1(t *testing.T) {
	now := time.Date(2025, 12, 15, 0, 0, 0, 0, time.Local)
	first, last := monthRange(now, 1)

	if first.Year() != 2026 || first.Month() != 1 || first.Day() != 1 {
		t.Errorf("monthRange Dec +1 first: got %v, want 2026-01-01", first)
	}
	if last.Year() != 2026 || last.Month() != 1 || last.Day() != 31 {
		t.Errorf("monthRange Dec +1 last: got %v, want 2026-01-31", last)
	}
}

func TestMonthRangeMinus13(t *testing.T) {
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.Local)
	first, last := monthRange(now, -13)

	if first.Year() != 2024 || first.Month() != 12 || first.Day() != 1 {
		t.Errorf("monthRange Jan -13 first: got %v, want 2024-12-01", first)
	}
	if last.Year() != 2024 || last.Month() != 12 || last.Day() != 31 {
		t.Errorf("monthRange Jan -13 last: got %v, want 2024-12-31", last)
	}
}

func TestMonthRangeLeapFeb(t *testing.T) {
	now := time.Date(2024, 2, 1, 0, 0, 0, 0, time.Local)
	first, last := monthRange(now, 0)

	if first.Month() != 2 || first.Year() != 2024 || first.Day() != 1 {
		t.Errorf("monthRange Feb 2024 first: got %v", first)
	}
	if last.Day() != 29 {
		t.Errorf("monthRange Feb 2024 last: got day %d, want 29", last.Day())
	}
}

// ---- weekRange ------------------------------------------------------------

func TestWeekRangeMondayStart(t *testing.T) {
	tests := []struct {
		name       string
		date       time.Time
		wantMonday time.Time
		wantSunday time.Time
	}{
		{
			"Monday itself",
			time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local),
		},
		{
			"Wednesday of same week",
			time.Date(2026, 8, 5, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local),
		},
		{
			"Sunday of same week",
			time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local),
		},
		{
			"Saturday of same week",
			time.Date(2026, 8, 8, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local),
			time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			monday, sunday := weekRange(tc.date, 0)
			if !monday.Equal(tc.wantMonday) {
				t.Errorf("weekRange monday: got %v, want %v", monday, tc.wantMonday)
			}
			if !sunday.Equal(tc.wantSunday) {
				t.Errorf("weekRange sunday: got %v, want %v", sunday, tc.wantSunday)
			}
		})
	}
}

func TestWeekRangePreviousWeek(t *testing.T) {
	// Wednesday 2026-08-05, offset -1 → Mon 2026-07-27 – Sun 2026-08-02
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.Local)
	monday, sunday := weekRange(now, -1)
	wantMon := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	wantSun := time.Date(2026, 8, 2, 0, 0, 0, 0, time.Local)
	if !monday.Equal(wantMon) {
		t.Errorf("weekRange -1 monday: got %v, want %v", monday, wantMon)
	}
	if !sunday.Equal(wantSun) {
		t.Errorf("weekRange -1 sunday: got %v, want %v", sunday, wantSun)
	}
}

// ---- deleteEntry --------------------------------------------------------

func TestDeleteEntryRemovesRow(t *testing.T) {
	db := openTestDB(t)

	id, _, err := insertOpen(db, "borrable", "proj")
	if err != nil {
		t.Fatalf("insertOpen: %v", err)
	}

	if err := deleteEntry(db, int(id)); err != nil {
		t.Fatalf("deleteEntry: %v", err)
	}

	if _, err := getEntry(db, id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("deleteEntry: expected row gone, getEntry err = %v", err)
	}
}

func TestDeleteEntryNotFound(t *testing.T) {
	db := openTestDB(t)

	if err := deleteEntry(db, 4242); err == nil {
		t.Error("deleteEntry with missing id: expected error, got nil")
	}
}

// ---- updateEntry ------------------------------------------------------------

func TestUpdateEntryChangesProyecto(t *testing.T) {
	db := openTestDB(t)

	id, _, err := insertOpen(db, "tarea", "demo")
	if err != nil {
		t.Fatalf("insertOpen: %v", err)
	}

	entry, err := updateEntry(db, int(id), "", "v-max", "", "", "", 0)
	if err != nil {
		t.Fatalf("updateEntry: %v", err)
	}
	if entry.Proyecto != "v-max" {
		t.Errorf("updateEntry proyecto: got %q, want %q", entry.Proyecto, "v-max")
	}
	if entry.Descripcion != "tarea" {
		t.Errorf("updateEntry: descripcion should be unchanged, got %q", entry.Descripcion)
	}
}

func TestUpdateEntryRecomputesDuration(t *testing.T) {
	db := openTestDB(t)

	seedClosed(t, db, "x", "p",
		"2026-08-05T09:00:00.000000", "2026-08-05T10:00:00.000000", 1.0)

	var id int
	if err := db.QueryRow(`SELECT id FROM entries LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("select id: %v", err)
	}

	entry, err := updateEntry(db, id, "", "", "", "2026-08-05T12:00:00.000000", "", 0)
	if err != nil {
		t.Fatalf("updateEntry: %v", err)
	}
	if !entry.DuracionHH.Valid || absDiff(entry.DuracionHH.Float64, 3.0) > 0.001 {
		t.Errorf("updateEntry: duracion_hh = %v, want ~3.0", entry.DuracionHH)
	}
}

func TestUpdateEntryExplicitDuracionWins(t *testing.T) {
	db := openTestDB(t)

	seedClosed(t, db, "x", "p",
		"2026-08-05T09:00:00.000000", "2026-08-05T10:00:00.000000", 1.0)

	var id int
	if err := db.QueryRow(`SELECT id FROM entries LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("select id: %v", err)
	}

	entry, err := updateEntry(db, id, "", "", "", "", "", 5.0)
	if err != nil {
		t.Fatalf("updateEntry: %v", err)
	}
	if absDiff(entry.DuracionHH.Float64, 5.0) > 0.001 {
		t.Errorf("updateEntry: duracion_hh = %v, want 5.0", entry.DuracionHH.Float64)
	}
}

func TestUpdateEntryNoFields(t *testing.T) {
	db := openTestDB(t)

	id, _, err := insertOpen(db, "tarea", "p")
	if err != nil {
		t.Fatalf("insertOpen: %v", err)
	}
	if _, err := updateEntry(db, int(id), "", "", "", "", "", 0); err == nil {
		t.Error("updateEntry with no fields: expected error, got nil")
	}
}

func TestUpdateEntryNotFound(t *testing.T) {
	db := openTestDB(t)

	if _, err := updateEntry(db, 9999, "", "x", "", "", "", 0); err == nil {
		t.Error("updateEntry with missing id: expected error, got nil")
	}
}

// ---- listClosed proyecto filter -------------------------------------------

func TestListClosedProyectoFilter(t *testing.T) {
	db := openTestDB(t)

	seedClosed(t, db, "a", "demo",
		"2026-08-03T09:00:00.000000", "2026-08-03T10:00:00.000000", 1.0)
	seedClosed(t, db, "b", "v-max",
		"2026-08-03T09:00:00.000000", "2026-08-03T10:00:00.000000", 1.0)

	rows, err := listClosed(db, "", "", "demo")
	if err != nil {
		t.Fatalf("listClosed: %v", err)
	}
	if len(rows) != 1 || rows[0].Proyecto != "demo" {
		t.Fatalf("listClosed proyecto filter: got %+v", rows)
	}
}

// ---- utility --------------------------------------------------------------

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestEntryMutationsRejectInvalidInputWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name, inicio, fin string
		duration          float64
	}{
		{"invalid", "broken", "2026-08-01T10:00:00", 0},
		{"invalid end", "2026-08-01T09:00:00", "broken", 0},
		{"invalid clock", "25:00", "26:00", 0},
		{"reversed explicit", "2026-08-01T11:00:00", "2026-08-01T10:00:00", 2},
		{"range overflow", "1000-08-01T09:00:00", "3000-08-01T10:00:00", 0},
		{"reversed", "2026-08-01T11:00:00", "2026-08-01T10:00:00", 0},
		{"negative", "", "", -1}, {"nan", "", "", math.NaN()},
		{"infinite", "", "", math.Inf(1)}, {"overflow", "", "", 1e20},
		{"partial range", "2026-08-01T09:00:00", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedClosed(t, db, "original", "project", "2026-08-01T09:00:00", "2026-08-01T10:00:00", 1)
			before, _ := getEntry(db, 1)
			if _, err := updateEntry(db, 1, "changed", "", tc.inicio, tc.fin, "", tc.duration); tc.name != "partial range" && err == nil {
				t.Fatal("update accepted invalid input")
			}
			after, _ := getEntry(db, 1)
			if tc.name != "partial range" && after != before {
				t.Fatalf("failed update changed entry: %+v", after)
			}
			if _, err := insertManual(db, "invalid", "project", tc.inicio, tc.fin, tc.duration, ""); err == nil {
				t.Fatal("manual accepted invalid input")
			}
			var count int
			db.QueryRow("SELECT count(*) FROM entries").Scan(&count)
			if count != 1 {
				t.Fatalf("invalid manual wrote row: count=%d", count)
			}
		})
	}
}

func TestConcurrentCloseFirstWins(t *testing.T) {
	db := openTestDB(t)
	id, _, err := insertOpen(db, "task", "project")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 20
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := closeEntry(db, int(id), "winner"); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful closes=%d, want 1", successes)
	}
}

func TestDeleteEntryPreservesMetricsAndClearsLink(t *testing.T) {
	db := openTestDB(t)
	id, _, _ := insertOpen(db, "task", "project")
	_, err := db.Exec("INSERT INTO responses (ts, proyecto, cliente, modelo, entry_id, source, external_id) VALUES ('2026-08-01T09:00:00','project','test','test',?,'test','one')", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteEntry(db, int(id)); err != nil {
		t.Fatal(err)
	}
	var link sql.NullInt64
	if err := db.QueryRow("SELECT entry_id FROM responses").Scan(&link); err != nil {
		t.Fatal(err)
	}
	if link.Valid {
		t.Fatalf("deleted entry still linked: %+v", link)
	}
}

func TestCloseRejectsFutureStart(t *testing.T) {
	db := openTestDB(t)
	id, _, _ := insertOpen(db, "task", "project")
	future := time.Now().Add(time.Hour).Format(tsLayout)
	db.Exec("UPDATE entries SET inicio=? WHERE id=?", future, id)
	if _, err := closeEntry(db, int(id), ""); err == nil {
		t.Fatal("closed future entry")
	}
	e, _ := getEntry(db, id)
	if e.Estado != "abierto" || e.Fin.Valid {
		t.Fatalf("failed close mutated entry: %+v", e)
	}
}

func TestUpdateFinDoesNotImplicitlyClose(t *testing.T) {
	db := openTestDB(t)
	id, _, err := insertOpen(db, "task", "project")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := updateEntry(db, int(id), "", "", "2026-08-01T09:00:00", "2026-08-01T10:00:00", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Estado != "abierto" || entry.DuracionHH.Float64 != 1 {
		t.Fatalf("unexpected update: %+v", entry)
	}
}

func TestUpdateRollbackOnDatabaseFailure(t *testing.T) {
	db := openTestDB(t)
	seedClosed(t, db, "original", "project", "2026-08-01T09:00:00", "2026-08-01T10:00:00", 1)
	before, _ := getEntry(db, 1)
	_, err := db.Exec(`CREATE TRIGGER reject_duration BEFORE UPDATE OF duracion_hh ON entries BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updateEntry(db, 1, "changed", "", "", "2026-08-01T11:00:00", "", 0); err == nil {
		t.Fatal("update unexpectedly succeeded")
	}
	after, _ := getEntry(db, 1)
	if after != before {
		t.Fatalf("failed update persisted partial fields: %+v", after)
	}
}

func TestDeleteRollbackOnLinkFailure(t *testing.T) {
	db := openTestDB(t)
	id, _, _ := insertOpen(db, "original", "project")
	_, err := db.Exec(`INSERT INTO responses (ts, proyecto, cliente, modelo, source, external_id, entry_id) VALUES ('2026-08-01T09:30:00','project','test','test','test','one',?)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER reject_link BEFORE UPDATE OF entry_id ON responses BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteEntry(db, int(id)); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	if _, err := getEntry(db, id); err != nil {
		t.Fatalf("entry lost despite failed deletion: %v", err)
	}
	var link int64
	if err := db.QueryRow("SELECT entry_id FROM responses").Scan(&link); err != nil || link != id {
		t.Fatalf("link changed despite failure: %d %v", link, err)
	}
}
