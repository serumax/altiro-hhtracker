package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Config holds runtime settings for the background worker and ingesters.
type Config struct {
	OpencodeDBPath    string
	ClaudeProjectsDir string
	IdleCloseMinutes  int
	IngestEnabled     bool
	TickInterval      time.Duration
}

// configFile is the on-disk overlay shape at ~/.hh-tracker/config.json.
type configFile struct {
	IdleCloseMinutes  *int    `json:"idle_close_minutes"`
	IngestEnabled     *bool   `json:"ingest_enabled"`
	OpencodeDBPath    *string `json:"opencode_db_path"`
	ClaudeProjectsDir *string `json:"claude_projects_dir"`
}

// loadConfig returns defaults, overlaid with any present keys from
// ~/.hh-tracker/config.json. A missing or malformed file yields the defaults.
func loadConfig(home string) Config {
	cfg := Config{
		OpencodeDBPath:    filepath.Join(home, ".local", "share", "opencode", "opencode.db"),
		ClaudeProjectsDir: filepath.Join(home, ".claude", "projects"),
		IdleCloseMinutes:  10,
		IngestEnabled:     true,
		TickInterval:      60 * time.Second,
	}

	path := filepath.Join(home, ".hh-tracker", "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	var f configFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("loadConfig: ignoring malformed %s: %v", path, err)
		return cfg
	}

	if f.IdleCloseMinutes != nil {
		cfg.IdleCloseMinutes = *f.IdleCloseMinutes
	}
	if f.IngestEnabled != nil {
		cfg.IngestEnabled = *f.IngestEnabled
	}
	if f.OpencodeDBPath != nil {
		cfg.OpencodeDBPath = *f.OpencodeDBPath
	}
	if f.ClaudeProjectsDir != nil {
		cfg.ClaudeProjectsDir = *f.ClaudeProjectsDir
	}
	return cfg
}
