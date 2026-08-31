package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// computeIdleCloses returns the ids of open entries whose last activity is older
// than now-threshold. When an entry has no entry in lastActivity, its Inicio
// timestamp is used as the fallback signal.
func computeIdleCloses(open []Entry, lastActivity map[int64]time.Time, now time.Time, threshold time.Duration) []int64 {
	cutoff := now.Add(-threshold)
	var ids []int64
	for _, e := range open {
		act, ok := lastActivity[e.ID]
		if !ok {
			t, err := parseTS(e.Inicio)
			if err != nil {
				continue
			}
			act = t
		}
		if act.Before(cutoff) {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// lastActivityFor returns the most recent activity timestamp known for an entry:
// the max of its Inicio, the newest ingested response ts (any proyecto — any
// agent response means the machine is in use), and the global last_tool_call_at
// meta value.
func lastActivityFor(db *sql.DB, entry Entry) time.Time {
	var best time.Time
	consider := func(s string) {
		if s == "" {
			return
		}
		if t, err := parseTS(s); err == nil && t.After(best) {
			best = t
		}
	}

	consider(entry.Inicio)

	var respTS sql.NullString
	if err := db.QueryRow(
		`SELECT MAX(ts) FROM responses`,
	).Scan(&respTS); err == nil && respTS.Valid {
		consider(respTS.String)
	}

	if v, err := getMeta(db, "last_tool_call_at"); err == nil {
		consider(v)
	}

	return best
}

// runWorkerOnce performs one ingest + idle-close pass. Ingest errors are logged
// but do not abort the pass. closeEntry filters estado='abierto', so repeated
// runs and concurrent processes are safe.
func runWorkerOnce(db *sql.DB, cfg Config, now time.Time) error {
	if cfg.IngestEnabled {
		if n, err := ingestOpencode(db, cfg.OpencodeDBPath, now); err != nil {
			log.Printf("worker: ingestOpencode: %v", err)
		} else if n > 0 {
			log.Printf("worker: ingested %d opencode responses", n)
		}
		if n, err := ingestClaude(db, cfg.ClaudeProjectsDir, now); err != nil {
			log.Printf("worker: ingestClaude: %v", err)
		} else if n > 0 {
			log.Printf("worker: ingested %d claude responses", n)
		}
	}

	open, err := listOpen(db)
	if err != nil {
		return err
	}

	lastActivity := make(map[int64]time.Time, len(open))
	for _, e := range open {
		lastActivity[e.ID] = lastActivityFor(db, e)
	}

	threshold := time.Duration(cfg.IdleCloseMinutes) * time.Minute
	for _, id := range computeIdleCloses(open, lastActivity, now, threshold) {
		if _, err := closeEntry(db, int(id), "cerrada automáticamente por inactividad"); err != nil {
			log.Printf("worker: closeEntry #%d: %v", id, err)
		}
	}
	return nil
}

// startWorker runs runWorkerOnce on a ticker until the process exits.
func startWorker(db *sql.DB, cfg Config) {
	go func() {
		ticker := time.NewTicker(cfg.TickInterval)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("worker: recovered from panic: %v", r)
					}
				}()
				if err := runWorkerOnce(db, cfg, time.Now()); err != nil {
					log.Printf("worker: runWorkerOnce: %v", err)
				}
			}()
		}
	}()
}

// withActivity wraps a tool handler so every non-erroring call records the
// current time in the last_tool_call_at meta key.
func withActivity(db *sql.DB, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := h(ctx, req)
		if err == nil {
			if e := setMeta(db, "last_tool_call_at", time.Now().Format(tsLayout)); e != nil {
				log.Printf("withActivity: setMeta: %v", e)
			}
		}
		return res, err
	}
}
