package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	_ "modernc.org/sqlite"
)

//go:embed instructions.md
var instructions string

// version is overridden at build time by GoReleaser via -ldflags -X main.version.
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			installCmd()
			return
		case "init":
			initCmd()
			return
		case "version", "--version", "-v":
			fmt.Println(versionLine())
			return
		case "help", "--help", "-h":
			printHelp()
			return
		}
	}

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

	cfg := loadConfig(home)

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

	s.AddTool(mcp.NewTool("hh_delete",
		mcp.WithDescription("Borra una entrada por ID (abierta o cerrada)."),
		mcp.WithNumber("entry_id", mcp.Required(),
			mcp.Description("ID de la entrada a borrar.")),
	), hhDeleteHandler(db))

	s.AddTool(mcp.NewTool("hh_update",
		mcp.WithDescription("Actualiza campos de una entrada existente. Solo se pasan los campos a cambiar."),
		mcp.WithNumber("entry_id", mcp.Required(),
			mcp.Description("ID de la entrada a actualizar.")),
		mcp.WithString("descripcion",
			mcp.Description("Nueva descripción.")),
		mcp.WithString("proyecto",
			mcp.Description("Nuevo proyecto.")),
		mcp.WithString("inicio",
			mcp.Description("Nuevo inicio (ISO o HH:MM — se asume hoy).")),
		mcp.WithString("fin",
			mcp.Description("Nuevo fin (ISO o HH:MM — se asume hoy).")),
		mcp.WithString("resumen",
			mcp.Description("Nuevo resumen.")),
		mcp.WithNumber("duracion_hh",
			mcp.Description("Duración explícita en horas. Si se omite y hay inicio y fin, se recalcula.")),
	), hhUpdateHandler(db))

	s.AddTool(mcp.NewTool("hh_report",
		mcp.WithDescription("Lista las entradas cerradas en un rango de fechas opcional."),
		mcp.WithString("desde",
			mcp.Description("Fecha de inicio YYYY-MM-DD (inclusive).")),
		mcp.WithString("hasta",
			mcp.Description("Fecha de fin YYYY-MM-DD (inclusive).")),
		mcp.WithString("proyecto",
			mcp.Description("Filtra por proyecto (opcional).")),
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
		mcp.WithString("proyecto",
			mcp.Description("Filtra por proyecto (opcional).")),
	), hhSemanaHandler(db))

	s.AddTool(mcp.NewTool("hh_mes",
		mcp.WithDescription("Reporte de horas del mes."),
		mcp.WithNumber("offset",
			mcp.Description("Desplazamiento de mes (0=este mes, -1=mes pasado).")),
		mcp.WithString("proyecto",
			mcp.Description("Filtra por proyecto (opcional).")),
	), hhMesHandler(db))

	s.AddTool(mcp.NewTool("hh_costos",
		mcp.WithDescription("Reporte de costo y tokens de respuestas de agentes, agrupado por proyecto y modelo."),
		mcp.WithString("desde",
			mcp.Description("Fecha de inicio YYYY-MM-DD (inclusive).")),
		mcp.WithString("hasta",
			mcp.Description("Fecha de fin YYYY-MM-DD (inclusive).")),
		mcp.WithString("proyecto",
			mcp.Description("Filtra por proyecto (opcional).")),
		mcp.WithString("modelo",
			mcp.Description("Filtra por modelo (opcional).")),
	), hhCostosHandler(db))

	s.AddTool(mcp.NewTool("hh_tracking",
		mcp.WithDescription("Check shared project context using confirmed root human events. Never opens or closes hour entries. Initialize missing projects or resolve continuity questions using the owning claim token."),
		mcp.WithString("action", mcp.Description("check (default), initialize or resolve")),
		mcp.WithString("project", mcp.Description("Confirmed canonical project name; cwd marker takes precedence")),
		mcp.WithString("cwd", mcp.Description("Local workspace directory; initialized context is reused across chats")),
		mcp.WithString("client", mcp.Required()),
		mcp.WithString("chat", mcp.Required()),
		mcp.WithString("event_id", mcp.Required(), mcp.Description("Stable ID of the originating event, reused on retries; not a human session ID")),
		mcp.WithString("event_type", mcp.Description("human_prompt only for an actual human message; tool, stop and unknown never advance human time")),
		mcp.WithBoolean("confirmed_human", mcp.Description("Explicit provenance assertion, not inferred from a tool invocation")),
		mcp.WithBoolean("root", mcp.Description("Only the root orchestrator may assert ownership, never a subagent")),
		mcp.WithString("claim_token", mcp.Description("Owning chat token from the pending question; required to resolve")),
	), hhTrackingHandler(db, cfg))

	startWorker(db, cfg)

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
		) + entryOverlapWarning(db, id)), nil
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
		) + entryOverlapWarning(db, entry.ID)), nil
	}
}

func hhDeleteHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		entryID := req.GetInt("entry_id", 0)
		if entryID == 0 {
			return mcp.NewToolResultText("ERROR: entry_id es requerido."), nil
		}

		if err := deleteEntry(db, entryID); err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("🗑️  Entrada #%d borrada", entryID)), nil
	}
}

func hhUpdateHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		entryID := req.GetInt("entry_id", 0)
		if entryID == 0 {
			return mcp.NewToolResultText("ERROR: entry_id es requerido."), nil
		}

		descripcion := req.GetString("descripcion", "")
		proyecto := req.GetString("proyecto", "")
		inicio := req.GetString("inicio", "")
		fin := req.GetString("fin", "")
		resumen := req.GetString("resumen", "")
		duracionHH := req.GetFloat("duracion_hh", 0)

		entry, err := updateEntry(db, entryID, descripcion, proyecto, inicio, fin, resumen, duracionHH)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		durStr := ""
		if entry.DuracionHH.Valid {
			durStr = " — " + fmtDuracion(entry.DuracionHH.Float64)
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"✏️  Entrada #%d actualizada\n   %s [%s] %s%s",
			entry.ID, entry.Descripcion, entry.Proyecto, entry.Inicio[:16], durStr,
		) + entryOverlapWarning(db, entry.ID)), nil
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
		proyecto := req.GetString("proyecto", "")

		entries, err := listClosed(db, desde, hasta, proyecto)
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
		) + entryOverlapWarning(db, entry.ID)), nil
	}
}

func hhSemanaHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		offset := req.GetInt("offset", 0)
		proyecto := req.GetString("proyecto", "")
		now := time.Now()
		monday, sunday := weekRange(now, offset)

		desde := monday.Format("2006-01-02")
		hasta := sunday.Format("2006-01-02")

		entries, err := listReporte(db, desde, hasta, proyecto)
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
		proyecto := req.GetString("proyecto", "")
		now := time.Now()
		first, last := monthRange(now, offset)

		desde := first.Format("2006-01-02")
		hasta := last.Format("2006-01-02")

		entries, err := listReporte(db, desde, hasta, proyecto)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}

		// Title: "Agosto 2026"
		mesNombre := capitalizeFirst(mesesLargos[int(first.Month())])
		titulo := fmt.Sprintf("%s %d", mesNombre, first.Year())

		return mcp.NewToolResultText(reporte(entries, titulo)), nil
	}
}

func hhCostosHandler(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		desde := req.GetString("desde", "")
		hasta := req.GetString("hasta", "")
		proyecto := req.GetString("proyecto", "")
		modelo := req.GetString("modelo", "")

		rows, err := costReport(db, desde, hasta, proyecto, modelo)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}
		return mcp.NewToolResultText(formatCostos(rows)), nil
	}
}

// ---- Internal helpers -------------------------------------------------------

func versionLine() string {
	return "altiro-hhtracker " + version
}

func printHelp() {
	fmt.Println(versionLine())
	fmt.Println()
	fmt.Println("Servidor MCP para registrar horas de trabajo. Sin argumentos, corre por stdio.")
	fmt.Println()
	fmt.Println("Subcomandos:")
	fmt.Println("  install            Registra el servidor en Claude Code / OpenCode")
	fmt.Println("  init               Crea .hh-tracker.md en la raíz del repo actual")
	fmt.Println("  version|--version  Muestra la versión")
	fmt.Println("  help|--help        Muestra esta ayuda")
	fmt.Println()
	fmt.Println("Tools MCP: hh_start, hh_close, hh_status, hh_delete, hh_update,")
	fmt.Println("           hh_manual, hh_report, hh_semana, hh_mes, hh_costos")
}

// ---- init subcommand --------------------------------------------------------

// doInit writes a starter .hh-tracker.md at root unless one already exists.
// It never clobbers: created is false when the file is already there.
func doInit(root string) (created bool, path string, proyecto string, err error) {
	path = filepath.Join(root, projectFileName)
	if _, statErr := os.Stat(path); statErr == nil {
		return false, path, "", nil
	}

	proyecto = deriveProyecto(root)
	content := fmt.Sprintf("---\nproyecto: %s\nalias: []\n---\n", proyecto)
	if err = os.WriteFile(path, []byte(content), 0644); err != nil {
		return false, path, proyecto, err
	}
	return true, path, proyecto, nil
}

// initCmd resolves the main checkout root for the current directory (the
// primary checkout even when run from a linked worktree) and runs doInit there,
// so every worktree shares one .hh-tracker.md.
func initCmd() {
	root, err := mainCheckoutRoot(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: no estás dentro de un repositorio git.")
		os.Exit(1)
	}

	created, path, proyecto, err := doInit(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if created {
		fmt.Printf("✓ creado %s (proyecto: %s) — edítalo si el nombre o los alias no calzan\n", path, proyecto)
	} else {
		fmt.Printf("ya existe: %s\n", path)
	}
}

// ---- Install subcommand -------------------------------------------------------

func installCmd() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: no se pudo encontrar el home: %v\n", err)
		os.Exit(1)
	}

	found := false

	// Claude Code — ~/.claude.json exists
	claudeJSON := filepath.Join(home, ".claude.json")
	if _, err := os.Stat(claudeJSON); err == nil {
		if err := installClaudeCode(); err != nil {
			fmt.Printf("Claude Code: ERROR — %v\n", err)
		} else {
			fmt.Println("Claude Code: ✓ registrado")
			found = true
		}
	}

	// OpenCode — ~/.config/opencode/config.json exists
	opencodeCfg := filepath.Join(home, ".config", "opencode", "config.json")
	if _, err := os.Stat(opencodeCfg); err == nil {
		if err := installOpenCode(opencodeCfg); err != nil {
			fmt.Printf("OpenCode:    ERROR — %v\n", err)
		} else {
			fmt.Println("OpenCode:    ✓ registrado")
			found = true
		}
	}

	if !found {
		fmt.Println("No se detectó ningún cliente compatible (Claude Code, OpenCode).")
		fmt.Println("Agrega manualmente a tu cliente MCP:")
		fmt.Println(`  command: altiro-hhtracker`)
		return
	}

	fmt.Println("\nListo. Reinicia tu cliente para activar altiro-hhtracker.")
}

func installClaudeCode() error {
	// Remove first so re-runs are idempotent (mcp add fails if name exists).
	exec.Command("claude", "mcp", "remove", "altiro-hhtracker", "--scope", "user").Run()
	out, err := exec.Command("claude", "mcp", "add", "altiro-hhtracker", "--scope", "user", "--", "altiro-hhtracker").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func installOpenCode(cfgPath string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("leer config: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parsear config: %v", err)
	}

	if cfg["mcp"] == nil {
		cfg["mcp"] = map[string]any{}
	}
	mcpBlock, ok := cfg["mcp"].(map[string]any)
	if !ok {
		return fmt.Errorf("la clave 'mcp' en config.json no es un objeto")
	}

	mcpBlock["altiro-hhtracker"] = map[string]any{
		"type":    "local",
		"command": "altiro-hhtracker",
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("serializar config: %v", err)
	}
	return os.WriteFile(cfgPath, out, 0644)
}

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

// Warning only: recording remains successful and other rows are never altered.
func entryOverlapWarning(db *sql.DB, id int64) string {
	entry, err := getEntry(db, id)
	if err != nil {
		return "\nWarning: overlap check unavailable."
	}
	ids, err := overlappingEntryIDs(db, entry)
	if err != nil {
		return "\nWarning: overlap check unavailable."
	}
	if len(ids) == 0 {
		return ""
	}
	return fmt.Sprintf("\nWarning: time overlap with entries %v. Review human hours; no other entries were changed.", ids)
}

func hhTrackingHandler(db *sql.DB, cfg Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input := TrackingInput{
			Action: req.GetString("action", "check"), Project: req.GetString("project", ""), Cwd: req.GetString("cwd", ""),
			Client: req.GetString("client", ""), Chat: req.GetString("chat", ""), EventID: req.GetString("event_id", ""),
			EventType: req.GetString("event_type", "unknown"), ConfirmedHuman: req.GetBool("confirmed_human", false), Root: req.GetBool("root", false), ClaimToken: req.GetString("claim_token", ""),
		}
		result, err := trackProject(db, input, time.Now(), time.Duration(cfg.HumanReminderMinutes)*time.Minute)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}
		body, err := json.Marshal(result)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ERROR: %v", err)), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}
