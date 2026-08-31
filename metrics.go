package main

import (
	"database/sql"
)

// Response mirrors one row in the responses table.
type Response struct {
	Ts               string
	Proyecto         string
	Cliente          string
	Modelo           string
	DuracionS        sql.NullFloat64
	TokensIn         int64
	TokensOut        int64
	TokensCacheWrite int64
	TokensCacheRead  int64
	CostoUSD         float64
	EntryID          sql.NullInt64
	Source           string
	ExternalID       string
}

// CostRow is one aggregated (proyecto, modelo) group from costReport.
type CostRow struct {
	Proyecto      string
	Modelo        string
	TokensIn      int64
	TokensOut     int64
	SumCacheWrite int64
	SumCacheRead  int64
	CostoUSD      float64
	NumRespuestas int64
	DuracionS     float64
}

// insertResponse inserts a response row, ignoring duplicates by (source, external_id).
// inserted reflects whether a new row was actually written.
func insertResponse(db *sql.DB, r Response) (inserted bool, err error) {
	res, err := db.Exec(
		`INSERT INTO responses
		   (ts, proyecto, cliente, modelo, duracion_s, tokens_in, tokens_out,
		    tokens_cache_write, tokens_cache_read, costo_usd, entry_id, source, external_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source, external_id) DO NOTHING`,
		r.Ts, r.Proyecto, r.Cliente, r.Modelo, r.DuracionS,
		r.TokensIn, r.TokensOut, r.TokensCacheWrite, r.TokensCacheRead, r.CostoUSD,
		r.EntryID, r.Source, r.ExternalID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// getWatermark returns the stored watermark for source, or "" when absent.
func getWatermark(db *sql.DB, source string) (string, error) {
	var wm string
	err := db.QueryRow(`SELECT watermark FROM ingest_state WHERE source=?`, source).Scan(&wm)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return wm, err
}

// setWatermark upserts the watermark for source.
func setWatermark(db *sql.DB, source, wm string) error {
	_, err := db.Exec(
		`INSERT INTO ingest_state (source, watermark) VALUES (?, ?)
		 ON CONFLICT(source) DO UPDATE SET watermark=excluded.watermark`,
		source, wm,
	)
	return err
}

// getMeta returns the stored value for key, or "" when absent.
func getMeta(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// setMeta upserts the value for key.
func setMeta(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value,
	)
	return err
}

// singleOpenEntryID returns the id only when exactly one entry is open.
func singleOpenEntryID(db *sql.DB) (sql.NullInt64, error) {
	rows, err := db.Query(`SELECT id FROM entries WHERE estado='abierto' LIMIT 2`)
	if err != nil {
		return sql.NullInt64{}, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return sql.NullInt64{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return sql.NullInt64{}, err
	}
	if len(ids) == 1 {
		return sql.NullInt64{Int64: ids[0], Valid: true}, nil
	}
	return sql.NullInt64{}, nil
}

// costReport aggregates responses grouped by (proyecto, modelo), optionally
// filtered by a DATE(ts) range and by proyecto / modelo.
func costReport(db *sql.DB, desde, hasta, proyecto, modelo string) ([]CostRow, error) {
	q := `SELECT proyecto, modelo,
	             SUM(tokens_in), SUM(tokens_out),
	             SUM(tokens_cache_write), SUM(tokens_cache_read),
	             SUM(costo_usd), COUNT(*), COALESCE(SUM(duracion_s), 0)
	      FROM responses WHERE 1=1`
	var args []interface{}
	if desde != "" {
		q += ` AND DATE(ts) >= ?`
		args = append(args, desde)
	}
	if hasta != "" {
		q += ` AND DATE(ts) <= ?`
		args = append(args, hasta)
	}
	if proyecto != "" {
		q += ` AND proyecto = ?`
		args = append(args, proyecto)
	}
	if modelo != "" {
		q += ` AND modelo = ?`
		args = append(args, modelo)
	}
	q += ` GROUP BY proyecto, modelo ORDER BY proyecto ASC, modelo ASC`

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CostRow
	for rows.Next() {
		var c CostRow
		if err := rows.Scan(&c.Proyecto, &c.Modelo, &c.TokensIn, &c.TokensOut,
			&c.SumCacheWrite, &c.SumCacheRead, &c.CostoUSD, &c.NumRespuestas, &c.DuracionS); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
