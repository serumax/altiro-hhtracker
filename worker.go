package main

import (
	"database/sql"
	"log"
	"time"
)

// runWorkerOnce imports assistant metrics only. Silence, bot responses and tool
// calls cannot establish whether human work stopped, so it never closes HH.
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
