package main

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	_ "modernc.org/sqlite"
)

//go:embed instructions.md
var instructions string

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("UserHomeDir: %v", err)
	}

	dbPath := filepath.Join(home, ".hh-tracker", "hh.db")
	// Migration source: where the Python server kept the database.
	srcPath := filepath.Join(home, ".claude", "mcp-servers", "hh-tracker", "hh.db")

	// Ensure the destination directory exists before migration so that
	// migrateDB can create the file if the source is present.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Printf("mkdir %s: %v", filepath.Dir(dbPath), err)
	}
	if err := migrateDB(srcPath, dbPath); err != nil {
		log.Printf("migrateDB: %v", err)
	}

	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	s := server.NewMCPServer("altiro-hhtracker", "0.1.0",
		server.WithToolCapabilities(true),
		server.WithInstructions(instructions),
	)

	s.AddTool(mcp.NewTool("hh_start",
		mcp.WithDescription("Abre una nueva entrada de tiempo."),
		mcp.WithString("descripcion", mcp.Required(),
			mcp.Description("Descripción breve del trabajo.")),
		mcp.WithString("proyecto",
			mcp.Description("Nombre del proyecto (default: general).")),
	), hhStartHandler(db))

	s.AddTool(mcp.NewTool("hh_close",
		mcp.WithDescription("Cierra una entrada abierta y registra la duración total."),
		mcp.WithNumber("entry_id", mcp.Required(),
			mcp.Description("ID de la entrada abierta a cerrar.")),
		mcp.WithString("resumen",
			mcp.Description("Resumen opcional del trabajo realizado.")),
	), hhCloseHandler(db))

	s.AddTool(mcp.NewTool("hh_status",
		mcp.WithDescription("Lista todas las entradas actualmente abiertas."),
	), hhStatusHandler(db))

	s.AddTool(mcp.NewTool("hh_report",
		mcp.WithDescription("Lista las entradas cerradas en un rango de fechas opcional."),
		mcp.WithString("desde",
			mcp.Description("Fecha de inicio YYYY-MM-DD (inclusive).")),
		mcp.WithString("hasta",
			mcp.Description("Fecha de fin YYYY-MM-DD (inclusive).")),
	), hhReportHandler(db))

	s.AddTool(mcp.NewTool("hh_manual",
		mcp.WithDescription("Registra una entrada de tiempo manualmente."),
		mcp.WithString("descripcion", mcp.Required()),
		mcp.WithString("proyecto", mcp.Required()),
		mcp.WithString("inicio",
			mcp.Description("Inicio (ISO o HH:MM — se asume hoy).")),
		mcp.WithString("fin",
			mcp.Description("Fin (ISO o HH:MM — se asume hoy).")),
		mcp.WithNumber("duracion_hh",
			mcp.Description("Duración en horas fraccionarias.")),
		mcp.WithString("resumen",
			mcp.Description("Resumen opcional.")),
	), hhManualHandler(db))

	s.AddTool(mcp.NewTool("hh_semana",
		mcp.WithDescription("Reporte de horas de la semana."),
		mcp.WithNumber("offset",
			mcp.Description("Desplazamiento de semana (0=esta semana, -1=semana pasada).")),
	), hhSemanaHandler(db))

	s.AddTool(mcp.NewTool("hh_mes",
		mcp.WithDescription("Reporte de horas del mes."),
		mcp.WithNumber("offset",
			mcp.Description("Desplazamiento de mes (0=este mes, -1=mes pasado).")),
	), hhMesHandler(db))

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("ServeStdio: %v", err)
	}
}

// ---- Handler factories -------------------------------------------------------
// Each factory captures db and returns the ToolHandlerFunc that mcp-go calls.
// Errors are returned as plain text (NewToolResultText), never as Go errors,
// so the MCP client receives a readable message instead of an opaque failure.

func hhStartHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		descripcion, err := req.RequireString("descripcion")
		if err != nil {
			return mcp.NewToolResultText("ERROR: descripcion es requerido."), nil
		}
		proyecto := req.GetString("proyecto", "general")

		id, inicio, err := insertOpen(db, descripcion, proyecto)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf(
			"✅ Entrada #%d abierta\n   %s [%s] — %s",
			id, descripcion, proyecto, inicio[:16],
		)), nil
	}
}

func hhCloseHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		entryID := req.GetInt("entry_id", 0)
		if entryID == 0 {
			return mcp.NewToolResultText("ERROR: entry_id es requerido."), nil
		}
		resumen := req.GetString("resumen", "")

		entry, err := closeEntry(db, entryID, resumen)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		durStr := ""
		if entry.DuracionHH.Valid {
			durStr = " — " + fmtDuracion(entry.DuracionHH.Float64)
		}
		finStr := ""
		if entry.Fin.Valid {
			finStr = entry.Fin.String[:16]
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"✅ Entrada #%d cerrada\n   %s [%s]\n   %s → %s%s",
			entry.ID, entry.Descripcion, entry.Proyecto,
			entry.Inicio[:16], finStr, durStr,
		)), nil
	}
}

func hhStatusHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		entries, err := listOpen(db)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}
		if len(entries) == 0 {
			return mcp.NewToolResultText("No hay entradas abiertas."), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Entradas abiertas (%d):\n", len(entries))
		for _, e := range entries {
			sb.WriteString(fmt.Sprintf("#%d — %s [%s] desde %s\n",
				e.ID, e.Descripcion, e.Proyecto, e.Inicio[:16]))
		}
		return mcp.NewToolResultText(sb.String()), nil
	}
}

func hhReportHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		desde := req.GetString("desde", "")
		hasta := req.GetString("hasta", "")

		entries, err := listClosed(db, desde, hasta)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}
		if len(entries) == 0 {
			return mcp.NewToolResultText("Sin entradas cerradas."), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Entradas cerradas (%d):\n", len(entries))
		for _, e := range entries {
			durStr := ""
			if e.DuracionHH.Valid {
				durStr = " — " + fmtDuracion(e.DuracionHH.Float64)
			}
			sb.WriteString(fmt.Sprintf("#%d  %s [%s] %s%s\n",
				e.ID, e.Descripcion, e.Proyecto, e.Inicio[:16], durStr))
		}
		return mcp.NewToolResultText(sb.String()), nil
	}
}

func hhManualHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		descripcion, err := req.RequireString("descripcion")
		if err != nil {
			return mcp.NewToolResultText("ERROR: descripcion es requerido."), nil
		}
		proyecto, err := req.RequireString("proyecto")
		if err != nil {
			return mcp.NewToolResultText("ERROR: proyecto es requerido."), nil
		}
		inicio := req.GetString("inicio", "")
		fin := req.GetString("fin", "")
		duracionHH := req.GetFloat("duracion_hh", 0)
		resumen := req.GetString("resumen", "")

		// Normalize HH:MM times to full ISO timestamps using today's date.
		inicio = normalizeTimestamp(inicio)
		fin = normalizeTimestamp(fin)

		entry, err := insertManual(db, descripcion, proyecto, inicio, fin, duracionHH, resumen)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		durStr := ""
		if entry.DuracionHH.Valid {
			durStr = " — " + fmtDuracion(entry.DuracionHH.Float64)
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"✅ Entrada manual #%d registrada\n   %s [%s]%s",
			entry.ID, entry.Descripcion, entry.Proyecto, durStr,
		)), nil
	}
}

func hhSemanaHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		offset := req.GetInt("offset", 0)
		now := time.Now()
		monday, sunday := weekRange(now, offset)

		desde := monday.Format("2006-01-02")
		hasta := sunday.Format("2006-01-02")

		entries, err := listReporte(db, desde, hasta)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		// Title: "Semana del Lun 28/07 al Dom 03/08"
		wdMonIdx := (int(monday.Weekday()) + 6) % 7
		wdSunIdx := (int(sunday.Weekday()) + 6) % 7
		titulo := fmt.Sprintf("Semana del %s %02d/%02d al %s %02d/%02d",
			capitalizeFirst(diasCortos[wdMonIdx]), monday.Day(), int(monday.Month()),
			capitalizeFirst(diasCortos[wdSunIdx]), sunday.Day(), int(sunday.Month()),
		)

		return mcp.NewToolResultText(reporte(entries, titulo)), nil
	}
}

func hhMesHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		offset := req.GetInt("offset", 0)
		now := time.Now()
		first, last := monthRange(now, offset)

		desde := first.Format("2006-01-02")
		hasta := last.Format("2006-01-02")

		entries, err := listReporte(db, desde, hasta)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		// Title: "Agosto 2026"
		mesNombre := capitalizeFirst(mesesLargos[int(first.Month())])
		titulo := fmt.Sprintf("%s %d", mesNombre, first.Year())

		return mcp.NewToolResultText(reporte(entries, titulo)), nil
	}
}

// ---- Internal helpers -------------------------------------------------------

// normalizeTimestamp converts a bare HH:MM string to a full ISO timestamp
// using today's date. Strings that already contain a date separator are
// returned unchanged (including empty strings).
func normalizeTimestamp(s string) string {
	if s == "" || strings.Contains(s, "-") || strings.Contains(s, "T") {
		return s
	}
	// Assume HH:MM — prepend today's date.
	today := time.Now().Format("2006-01-02")
	return today + "T" + s + ":00.000000"
}
