package main

import (
	"context"
	"database/sql"
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
