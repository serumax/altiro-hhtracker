package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS entries (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	descripcion TEXT    NOT NULL,
	proyecto    TEXT    NOT NULL DEFAULT 'general',
	inicio      TEXT    NOT NULL,
	fin         TEXT,
	duracion_hh REAL,
	resumen     TEXT,
	estado      TEXT    NOT NULL DEFAULT 'abierto'
);

CREATE TABLE IF NOT EXISTS responses (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	ts           TEXT    NOT NULL,
	proyecto     TEXT    NOT NULL,
	cliente      TEXT    NOT NULL,
	modelo       TEXT    NOT NULL,
	duracion_s   REAL,
	tokens_in    INTEGER NOT NULL DEFAULT 0,
	tokens_out   INTEGER NOT NULL DEFAULT 0,
	tokens_cache_write INTEGER NOT NULL DEFAULT 0,
	tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
	costo_usd    REAL    NOT NULL DEFAULT 0,
	entry_id     INTEGER,
	source       TEXT    NOT NULL,
	external_id  TEXT    NOT NULL,
	UNIQUE(source, external_id)
);

CREATE TABLE IF NOT EXISTS ingest_state (
	source     TEXT PRIMARY KEY,
	watermark  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

// Entry mirrors one row in the entries table.
type Entry struct {
	ID          int64
	Descripcion string
	Proyecto    string
	Inicio      string
	Fin         sql.NullString
	DuracionHH  sql.NullFloat64
	Resumen     sql.NullString
	Estado      string
}

// openDB opens (or creates) the SQLite database at path, creating the parent
// directory if needed. It applies WAL mode and the entries schema.
func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(parentDir(path), 0755); err != nil {
		return nil, fmt.Errorf("openDB mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("openDB open: %w", err)
	}
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA synchronous=NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("openDB pragma: %w", err)
		}
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("openDB schema: %w", err)
	}
	return db, nil
}

// migrateDB copies srcPath to dstPath if src exists and dst does not.
// It is a no-op when src is absent or dst already exists.
func migrateDB(srcPath, dstPath string) error {
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return nil
	}
	if _, err := os.Stat(dstPath); err == nil {
		// Destination already present — do not overwrite.
		return nil
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("migrateDB open src: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("migrateDB create dst: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("migrateDB copy: %w", err)
	}
	return nil
}

// ---- Query helpers --------------------------------------------------------

// insertOpen inserts a new open entry and returns its id and formatted inicio.
func insertOpen(db *sql.DB, descripcion, proyecto string) (int64, string, error) {
	inicio := time.Now().Format(tsLayout)
	res, err := db.Exec(
		`INSERT INTO entries (descripcion, proyecto, inicio, estado) VALUES (?, ?, ?, 'abierto')`,
		descripcion, proyecto, inicio,
	)
	if err != nil {
		return 0, "", err
	}
	id, err := res.LastInsertId()
	return id, inicio, err
}

// closeEntry marks an open entry as cerrado, computes duracion_hh, and returns the updated entry.
func closeEntry(db *sql.DB, id int, resumen string) (Entry, error) {
	tx, err := db.Begin()
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback()
	entry, err := getEntryFrom(tx, int64(id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && entry.Estado != "abierto") {
		return Entry{}, fmt.Errorf("No se encontró la entrada #%d o ya está cerrada.", id)
	}
	if err != nil {
		return Entry{}, err
	}
	start, err := parseTS(entry.Inicio)
	if err != nil {
		return Entry{}, fmt.Errorf("invalid start: %w", err)
	}
	end := time.Now()
	hours, err := elapsedHours(start, end)
	if err != nil {
		return Entry{}, err
	}
	entry.Fin = sql.NullString{String: end.Format(tsLayout), Valid: true}
	entry.DuracionHH = sql.NullFloat64{Float64: hours, Valid: true}
	entry.Resumen = sql.NullString{String: resumen, Valid: resumen != ""}
	entry.Estado = "cerrado"
	res, err := tx.Exec(`UPDATE entries SET estado='cerrado', fin=?, duracion_hh=?, resumen=? WHERE id=? AND estado='abierto'`, entry.Fin, entry.DuracionHH, entry.Resumen, id)
	if err != nil {
		return Entry{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Entry{}, err
	}
	if n != 1 {
		return Entry{}, fmt.Errorf("entry #%d is already closed", id)
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// deleteEntry clears response links and removes the entry in one transaction.
// Response metrics remain intact.
func deleteEntry(db *sql.DB, id int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM entries WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("No se encontró la entrada #%d.", id)
	}
	if _, err := tx.Exec(`UPDATE responses SET entry_id=NULL WHERE entry_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// updateEntry patches fields atomically. Empty strings and zero duration mean
// unchanged. A positive explicit duration overrides elapsed time; otherwise
// complete timestamps recalculate duration. Updating fin does not close a row.
func updateEntry(db *sql.DB, id int, descripcion, proyecto, inicio, fin, resumen string, duracionHH float64) (Entry, error) {
	if err := validateDuration(duracionHH); err != nil {
		return Entry{}, err
	}
	if descripcion == "" && proyecto == "" && inicio == "" && fin == "" && resumen == "" && duracionHH == 0 {
		return Entry{}, fmt.Errorf("ERROR: no hay campos para actualizar.")
	}
	tx, err := db.Begin()
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback()
	entry, err := getEntryFrom(tx, int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("No se encontró la entrada #%d.", id)
	}
	if err != nil {
		return Entry{}, err
	}
	if descripcion != "" {
		entry.Descripcion = descripcion
	}
	if proyecto != "" {
		entry.Proyecto = proyecto
	}
	if resumen != "" {
		entry.Resumen = sql.NullString{String: resumen, Valid: true}
	}
	if inicio != "" {
		start, err := parseTS(normalizeTimestamp(inicio))
		if err != nil {
			return Entry{}, fmt.Errorf("invalid start: %w", err)
		}
		entry.Inicio = start.Format(tsLayout)
	}
	if fin != "" {
		end, err := parseTS(normalizeTimestamp(fin))
		if err != nil {
			return Entry{}, fmt.Errorf("invalid end: %w", err)
		}
		entry.Fin = sql.NullString{String: end.Format(tsLayout), Valid: true}
	}
	start, err := parseTS(entry.Inicio)
	if err != nil {
		return Entry{}, fmt.Errorf("invalid start: %w", err)
	}
	if entry.Fin.Valid {
		end, err := parseTS(entry.Fin.String)
		if err != nil {
			return Entry{}, fmt.Errorf("invalid end: %w", err)
		}
		hours, err := elapsedHours(start, end)
		if err != nil {
			return Entry{}, err
		}
		if duracionHH == 0 {
			entry.DuracionHH = sql.NullFloat64{Float64: hours, Valid: true}
		}
	}
	if duracionHH > 0 {
		entry.DuracionHH = sql.NullFloat64{Float64: duracionHH, Valid: true}
	}
	_, err = tx.Exec(`UPDATE entries SET descripcion=?, proyecto=?, inicio=?, fin=?, resumen=?, duracion_hh=? WHERE id=?`, entry.Descripcion, entry.Proyecto, entry.Inicio, entry.Fin, entry.Resumen, entry.DuracionHH, id)
	if err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// elapsedHours refuses reversed intervals and saturated time.Sub results.
func elapsedHours(start, end time.Time) (float64, error) {
	elapsed := end.Sub(start)
	if end.Before(start) || !start.Add(elapsed).Equal(end) {
		return 0, fmt.Errorf("time interval must be ordered and representable")
	}
	return elapsed.Hours(), nil
}

// validateDuration also bounds conversion to a Go time.Duration for manual hours.
func validateDuration(hours float64) error {
	if math.IsNaN(hours) || math.IsInf(hours, 0) || hours < 0 || hours >= float64(math.MaxInt64)/float64(time.Hour) {
		return fmt.Errorf("duration must be finite, nonnegative and representable")
	}
	return nil
}

// listOpen returns all open entries ordered by inicio DESC.
func listOpen(db *sql.DB) ([]Entry, error) {
	rows, err := db.Query(
		`SELECT id, descripcion, proyecto, inicio, fin, duracion_hh, resumen, estado
		 FROM entries WHERE estado='abierto' ORDER BY inicio DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// listClosed returns closed entries, optionally filtered by date range and
// proyecto, ordered DESC.
func listClosed(db *sql.DB, desde, hasta, proyecto string) ([]Entry, error) {
	query, args := closedQuery("ORDER BY inicio DESC", desde, hasta, proyecto)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// listReporte returns closed entries in the date range ordered ASC (for hh_semana/hh_mes),
// optionally filtered by proyecto.
func listReporte(db *sql.DB, desde, hasta, proyecto string) ([]Entry, error) {
	query, args := closedQuery("ORDER BY inicio ASC", desde, hasta, proyecto)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// insertManual handles hh_manual Mode A (inicio+fin) and Mode B (duracion_hh only).
// Accepts local timestamps or HH:MM, or a positive duration alone.
func insertManual(db *sql.DB, descripcion, proyecto, inicio, fin string, duracionHH float64, resumen string) (Entry, error) {
	if err := validateDuration(duracionHH); err != nil {
		return Entry{}, err
	}
	if (inicio == "") != (fin == "") {
		return Entry{}, fmt.Errorf("provide both start and end, or duration alone")
	}
	inicio, fin = normalizeTimestamp(inicio), normalizeTimestamp(fin)
	var inicioStr, finStr string
	var durHH float64

	if inicio != "" && fin != "" {
		// Mode A: compute duration from timestamps.
		inicioT, err := parseTS(inicio)
		if err != nil {
			return Entry{}, fmt.Errorf("insertManual parse inicio: %w", err)
		}
		finT, err := parseTS(fin)
		if err != nil {
			return Entry{}, fmt.Errorf("insertManual parse fin: %w", err)
		}
		durHH, err = elapsedHours(inicioT, finT)
		if err != nil {
			return Entry{}, err
		}
		inicioStr = inicioT.Format(tsLayout)
		finStr = finT.Format(tsLayout)
	} else if duracionHH > 0 {
		// Mode B: back-compute inicio from now.
		finT := time.Now()
		inicioT := finT.Add(-time.Duration(float64(time.Second) * duracionHH * 3600))
		inicioStr = inicioT.Format(tsLayout)
		finStr = finT.Format(tsLayout)
		durHH = duracionHH
	} else {
		return Entry{}, fmt.Errorf("ERROR: pasa inicio+fin o duracion_hh.")
	}

	res, err := db.Exec(
		`INSERT INTO entries (descripcion, proyecto, inicio, fin, duracion_hh, resumen, estado)
		 VALUES (?, ?, ?, ?, ?, ?, 'cerrado')`,
		descripcion, proyecto, inicioStr, finStr, durHH, nullableString(resumen),
	)
	if err != nil {
		return Entry{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Entry{}, err
	}
	return getEntry(db, id)
}

// monthRange computes the first and last day of the month at offset months from now.
// It uses time.Date normalization so negative offsets crossing year boundaries work correctly.
func monthRange(now time.Time, offset int) (first, last time.Time) {
	first = time.Date(now.Year(), now.Month()+time.Month(offset), 1, 0, 0, 0, 0, now.Location())
	// AddDate(0,1,-1) gives the last day of first's month.
	last = first.AddDate(0, 1, -1)
	return first, last
}

// weekRange computes the Monday and Sunday of the week containing now, shifted by offset weeks.
// It uses the Monday-first conversion: isoWeekday = (Go.Weekday + 6) % 7.
func weekRange(now time.Time, offset int) (monday, sunday time.Time) {
	isoWD := (int(now.Weekday()) + 6) % 7 // 0=Monday … 6=Sunday
	monday = time.Date(now.Year(), now.Month(), now.Day()-isoWD+offset*7, 0, 0, 0, 0, now.Location())
	sunday = monday.AddDate(0, 0, 6)
	return monday, sunday
}

// ---- Internal helpers -----------------------------------------------------

func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

func parseTS(s string) (time.Time, error) {
	t, err := time.ParseInLocation(tsLayout, s, time.Local)
	if err == nil {
		return t, nil
	}
	// Fallback: without fractional seconds.
	return time.ParseInLocation("2006-01-02T15:04:05", s, time.Local)
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func closedQuery(order, desde, hasta, proyecto string) (string, []interface{}) {
	base := `SELECT id, descripcion, proyecto, inicio, fin, duracion_hh, resumen, estado
	         FROM entries WHERE estado='cerrado'`
	var args []interface{}
	if desde != "" && hasta != "" {
		base += ` AND DATE(inicio) >= ? AND DATE(inicio) <= ?`
		args = append(args, desde, hasta)
	} else if desde != "" {
		base += ` AND DATE(inicio) >= ?`
		args = append(args, desde)
	} else if hasta != "" {
		base += ` AND DATE(inicio) <= ?`
		args = append(args, hasta)
	}
	if proyecto != "" {
		base += ` AND proyecto = ?`
		args = append(args, proyecto)
	}
	return base + " " + order, args
}

func scanEntries(rows *sql.Rows) ([]Entry, error) {
	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Descripcion, &e.Proyecto, &e.Inicio,
			&e.Fin, &e.DuracionHH, &e.Resumen, &e.Estado); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func getEntry(db *sql.DB, id int64) (Entry, error) {
	return getEntryFrom(db, id)
}

func getEntryFrom(q interface{ QueryRow(string, ...any) *sql.Row }, id int64) (Entry, error) {
	var e Entry
	err := q.QueryRow(
		`SELECT id, descripcion, proyecto, inicio, fin, duracion_hh, resumen, estado
		 FROM entries WHERE id=?`, id,
	).Scan(&e.ID, &e.Descripcion, &e.Proyecto, &e.Inicio,
		&e.Fin, &e.DuracionHH, &e.Resumen, &e.Estado)
	return e, err
}

// overlappingEntryIDs reports human-time overlaps across projects without
// changing entries. An open interval has no known end; adjacency is not overlap.
func overlappingEntryIDs(db *sql.DB, entry Entry) ([]int64, error) {
	start, err := parseTS(entry.Inicio)
	if err != nil {
		return nil, err
	}
	var end time.Time
	if entry.Fin.Valid {
		end, err = parseTS(entry.Fin.String)
		if err != nil {
			return nil, err
		}
		if !end.After(start) {
			return nil, nil
		}
	}
	rows, err := db.Query(`SELECT id, inicio, fin FROM entries WHERE id<>?`, entry.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var otherStartString string
		var otherEndString sql.NullString
		if err := rows.Scan(&id, &otherStartString, &otherEndString); err != nil {
			return nil, err
		}
		otherStart, err := parseTS(otherStartString)
		if err != nil || (!end.IsZero() && !otherStart.Before(end)) {
			continue
		}
		if otherEndString.Valid {
			otherEnd, err := parseTS(otherEndString.String)
			if err != nil || !otherEnd.After(otherStart) || !start.Before(otherEnd) {
				continue
			}
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
