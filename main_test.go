package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// newTestDB opens a fresh SQLite DB in a temp dir and registers cleanup.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// callReq builds a minimal CallToolRequest with the given arguments map.
func callReq(args map[string]any) mcp.CallToolRequest {
	var r mcp.CallToolRequest
	r.Params.Arguments = args
	return r
}

// textOf extracts the first TextContent string from a CallToolResult.
func textOf(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil {
		t.Fatal("result is nil")
	}
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("no TextContent in result: %+v", result.Content)
	return ""
}

// ---- hh_start ---------------------------------------------------------------

func TestHhStart_ReturnsEntryID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhStartHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "implementar login",
		"proyecto":    "demo-app",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "implementar login") {
		t.Errorf("expected descripcion in output, got: %q", text)
	}
	if !strings.Contains(text, "demo-app") {
		t.Errorf("expected proyecto in output, got: %q", text)
	}
	if !strings.Contains(text, "#1") {
		t.Errorf("expected entry id #1 in output, got: %q", text)
	}
}

func TestHhStart_DefaultsProyectoToGeneral(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhStartHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "tarea sin proyecto",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "general") {
		t.Errorf("expected 'general' proyecto in output, got: %q", text)
	}
}

func TestHhStart_MissingDescripcionReturnsError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhStartHandler(db)(ctx, callReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(strings.ToUpper(text), "ERROR") {
		t.Errorf("expected error message, got: %q", text)
	}
}

// ---- hh_status --------------------------------------------------------------

func TestHhStatus_EmptyDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhStatusHandler(db)(ctx, callReq(nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "No hay") {
		t.Errorf("expected 'No hay' for empty status, got: %q", text)
	}
}

func TestHhStatus_ShowsOpenEntry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Open an entry first.
	_, _ = hhStartHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "revisar PRs",
		"proyecto":    "general",
	}))

	result, err := hhStatusHandler(db)(ctx, callReq(nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "revisar PRs") {
		t.Errorf("expected open entry in status, got: %q", text)
	}
}

// ---- hh_close ---------------------------------------------------------------

func TestHhClose_ClosesEntryAndReportsDuration(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Open an entry.
	_, _ = hhStartHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "escribir tests",
		"proyecto":    "hh-tracker",
	}))

	result, err := hhCloseHandler(db)(ctx, callReq(map[string]any{
		"entry_id": float64(1),
		"resumen":  "tests written",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "cerrad") { // cerrada or cerrado
		t.Errorf("expected 'cerrad*' in output, got: %q", text)
	}
	if !strings.Contains(text, "#1") {
		t.Errorf("expected entry id #1 in close output, got: %q", text)
	}
}

func TestHhClose_NonExistentEntryReturnsError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhCloseHandler(db)(ctx, callReq(map[string]any{
		"entry_id": float64(999),
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "999") {
		t.Errorf("expected entry id 999 in error message, got: %q", text)
	}
}

// ---- hh_report --------------------------------------------------------------

func TestHhReport_EmptyReturnsNonEmptyMessage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhReportHandler(db)(ctx, callReq(nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if text == "" {
		t.Error("expected non-empty output from hh_report")
	}
}

// ---- version --------------------------------------------------------------

func TestVersionLine_IncludesBinaryName(t *testing.T) {
	line := versionLine()
	if !strings.Contains(line, "altiro-hhtracker") {
		t.Errorf("versionLine: got %q, want it to contain 'altiro-hhtracker'", line)
	}
	if !strings.Contains(line, version) {
		t.Errorf("versionLine: got %q, want it to contain version %q", line, version)
	}
}

// ---- hh_delete -------------------------------------------------------------

func TestHhDelete_RemovesEntry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, _ = hhStartHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "borrar esto",
		"proyecto":    "p",
	}))

	result, err := hhDeleteHandler(db)(ctx, callReq(map[string]any{
		"entry_id": float64(1),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(textOf(t, result), "#1") {
		t.Errorf("expected #1 in delete output, got: %q", textOf(t, result))
	}

	st, _ := hhStatusHandler(db)(ctx, callReq(nil))
	if !strings.Contains(textOf(t, st), "No hay") {
		t.Errorf("expected no open entries after delete, got: %q", textOf(t, st))
	}
}

func TestHhDelete_NonExistentReturnsError(t *testing.T) {
	db := newTestDB(t)

	result, err := hhDeleteHandler(db)(context.Background(), callReq(map[string]any{
		"entry_id": float64(777),
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !strings.Contains(textOf(t, result), "777") {
		t.Errorf("expected id 777 in error message, got: %q", textOf(t, result))
	}
}

func TestHhDelete_MissingEntryIDReturnsError(t *testing.T) {
	db := newTestDB(t)

	result, _ := hhDeleteHandler(db)(context.Background(), callReq(map[string]any{}))
	if !strings.Contains(strings.ToUpper(textOf(t, result)), "ERROR") {
		t.Errorf("expected error for missing entry_id, got: %q", textOf(t, result))
	}
}

// ---- hh_update ------------------------------------------------------------

func TestHhUpdate_ChangesProyecto(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, _ = hhStartHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "tarea",
		"proyecto":    "demo",
	}))

	result, err := hhUpdateHandler(db)(ctx, callReq(map[string]any{
		"entry_id": float64(1),
		"proyecto": "v-max",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(textOf(t, result), "v-max") {
		t.Errorf("expected v-max in output, got: %q", textOf(t, result))
	}
}

func TestHhUpdate_MissingEntryIDReturnsError(t *testing.T) {
	db := newTestDB(t)

	result, _ := hhUpdateHandler(db)(context.Background(), callReq(map[string]any{
		"proyecto": "x",
	}))
	if !strings.Contains(strings.ToUpper(textOf(t, result)), "ERROR") {
		t.Errorf("expected error for missing entry_id, got: %q", textOf(t, result))
	}
}

func TestHhUpdate_NoFieldsReturnsError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, _ = hhStartHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "tarea",
		"proyecto":    "p",
	}))

	result, _ := hhUpdateHandler(db)(ctx, callReq(map[string]any{
		"entry_id": float64(1),
	}))
	if !strings.Contains(strings.ToUpper(textOf(t, result)), "ERROR") {
		t.Errorf("expected error when no fields given, got: %q", textOf(t, result))
	}
}

// ---- hh_report proyecto filter -------------------------------------------

func TestHhReport_ProyectoFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, _ = hhManualHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "trabajo demo", "proyecto": "demo", "duracion_hh": float64(1),
	}))
	_, _ = hhManualHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "trabajo vmax", "proyecto": "v-max", "duracion_hh": float64(1),
	}))

	result, err := hhReportHandler(db)(ctx, callReq(map[string]any{
		"proyecto": "demo",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "trabajo demo") {
		t.Errorf("expected demo entry in output, got: %q", text)
	}
	if strings.Contains(text, "trabajo vmax") {
		t.Errorf("did not expect v-max entry in filtered output, got: %q", text)
	}
}

// ---- hh_manual --------------------------------------------------------------

func TestHhManual_ModeB_DuracionHH(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhManualHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "reunión de equipo",
		"proyecto":    "general",
		"duracion_hh": float64(1.5),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "reunión de equipo") {
		t.Errorf("expected descripcion in output, got: %q", text)
	}
}

func TestHhManual_ModeA_InicioFin(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhManualHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "standup",
		"proyecto":    "general",
		"inicio":      "09:00",
		"fin":         "09:30",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(text, "standup") {
		t.Errorf("expected descripcion in output, got: %q", text)
	}
}

func TestHhManual_MissingBothModesReturnsError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhManualHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "sin tiempo",
		"proyecto":    "general",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	text := textOf(t, result)
	if !strings.Contains(strings.ToUpper(text), "ERROR") {
		t.Errorf("expected error when neither inicio/fin nor duracion_hh provided, got: %q", text)
	}
}

// ---- hh_semana / hh_mes -----------------------------------------------------

func TestHhSemana_EmptyReturnsNonEmptyMessage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhSemanaHandler(db)(ctx, callReq(nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if text == "" {
		t.Error("expected non-empty output from hh_semana")
	}
}

func TestHhMes_EmptyReturnsNonEmptyMessage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := hhMesHandler(db)(ctx, callReq(nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if text == "" {
		t.Error("expected non-empty output from hh_mes")
	}
}

func TestHhSemana_WithClosedEntry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Create and close an entry manually.
	_, _ = hhManualHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "trabajo de la semana",
		"proyecto":    "test",
		"duracion_hh": float64(2.0),
	}))

	result, err := hhSemanaHandler(db)(ctx, callReq(map[string]any{
		"offset": float64(0),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if text == "" {
		t.Error("expected non-empty output from hh_semana after inserting entry")
	}
}

func TestHhMes_WithClosedEntry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Create a manual entry for this month.
	_, _ = hhManualHandler(db)(ctx, callReq(map[string]any{
		"descripcion": "trabajo del mes",
		"proyecto":    "test",
		"duracion_hh": float64(3.0),
	}))

	result, err := hhMesHandler(db)(ctx, callReq(map[string]any{
		"offset": float64(0),
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	if text == "" {
		t.Error("expected non-empty output from hh_mes after inserting entry")
	}
}

// ---- hh_costos -----------------------------------------------------------

func TestHhCostos_Empty(t *testing.T) {
	db := newTestDB(t)
	result, err := hhCostosHandler(db)(context.Background(), callReq(nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(textOf(t, result), "Sin respuestas registradas") {
		t.Errorf("expected empty message, got: %q", textOf(t, result))
	}
}

func TestHhCostos_AggregatesAndTotals(t *testing.T) {
	db := newTestDB(t)

	seed := []Response{
		{Ts: "2026-08-10T10:00:00.000000", Proyecto: "alpha", Cliente: "opencode", Modelo: "m1", TokensIn: 100, TokensOut: 200, TokensCacheWrite: 4, TokensCacheRead: 6, CostoUSD: 0.5, Source: "opencode", ExternalID: "1"},
		{Ts: "2026-08-11T10:00:00.000000", Proyecto: "alpha", Cliente: "opencode", Modelo: "m1", TokensIn: 50, TokensOut: 50, TokensCacheWrite: 0, TokensCacheRead: 0, CostoUSD: 0.25, Source: "opencode", ExternalID: "2"},
		{Ts: "2026-08-12T10:00:00.000000", Proyecto: "beta", Cliente: "claude-code", Modelo: "m2", TokensIn: 1, TokensOut: 1, TokensCacheWrite: 1, TokensCacheRead: 1, CostoUSD: 0, Source: "claude-code", ExternalID: "3"},
	}
	for _, r := range seed {
		if _, err := insertResponse(db, r); err != nil {
			t.Fatal(err)
		}
	}

	result, err := hhCostosHandler(db)(context.Background(), callReq(nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := textOf(t, result)
	for _, want := range []string{"alpha", "m1", "beta", "m2", "TOTAL", "0.75"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output, got:\n%s", want, text)
		}
	}

	// proyecto filter
	result, _ = hhCostosHandler(db)(context.Background(), callReq(map[string]any{"proyecto": "beta"}))
	text = textOf(t, result)
	if strings.Contains(text, "alpha") {
		t.Errorf("proyecto filter leaked alpha: %s", text)
	}
}

// ---- init subcommand -----------------------------------------------------

func TestDoInit_CreatesFileThenIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "my-cool-repo")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}

	created, path, proyecto, err := doInit(root)
	if err != nil {
		t.Fatalf("doInit: %v", err)
	}
	if !created {
		t.Error("first doInit: created=false, want true")
	}
	if path != filepath.Join(root, ".hh-tracker.md") {
		t.Errorf("path = %q", path)
	}
	// No git, no parent marker => deriveProyecto returns the basename.
	if proyecto != "my-cool-repo" {
		t.Errorf("proyecto = %q, want my-cool-repo", proyecto)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "proyecto: my-cool-repo") {
		t.Errorf("file contents missing proyecto line:\n%s", b)
	}
	if !strings.Contains(string(b), "alias: []") {
		t.Errorf("file contents missing alias line:\n%s", b)
	}

	// Second call: idempotent, no clobber.
	created2, _, _, err := doInit(root)
	if err != nil {
		t.Fatalf("second doInit: %v", err)
	}
	if created2 {
		t.Error("second doInit: created=true, want false")
	}
	b2, _ := os.ReadFile(path)
	if string(b2) != string(b) {
		t.Errorf("file bytes changed on second doInit:\nbefore=%q\nafter=%q", b, b2)
	}
}

func TestManualOverlapWarningPreservesOtherEntry(t *testing.T) {
	db := newTestDB(t)
	seedClosed(t, db, "existing", "other-project", "2026-08-01T09:00:00", "2026-08-01T10:00:00", 1)
	before, _ := getEntry(db, 1)
	result, err := hhManualHandler(db)(context.Background(), callReq(map[string]any{
		"descripcion": "new task", "proyecto": "project", "inicio": "2026-08-01T09:30:00", "fin": "2026-08-01T10:30:00",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if text := textOf(t, result); !strings.Contains(text, "overlap") {
		t.Fatalf("missing overlap warning: %s", text)
	}
	after, _ := getEntry(db, 1)
	if after != before {
		t.Fatal("overlap warning changed existing entry")
	}
	entries, _ := listClosed(db, "", "", "")
	if len(entries) != 2 {
		t.Fatalf("warning prevented recording: %d", len(entries))
	}
}

func TestManualAdjacentEntriesDoNotWarn(t *testing.T) {
	db := newTestDB(t)
	seedClosed(t, db, "existing", "project", "2026-08-01T09:00:00", "2026-08-01T10:00:00", 1)
	result, _ := hhManualHandler(db)(context.Background(), callReq(map[string]any{
		"descripcion": "new task", "proyecto": "project", "inicio": "2026-08-01T10:00:00", "fin": "2026-08-01T11:00:00",
	}))
	if text := textOf(t, result); strings.Contains(text, "overlap") {
		t.Fatalf("adjacent entries overlap: %s", text)
	}
}
