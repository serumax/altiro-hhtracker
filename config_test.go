package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	home := t.TempDir()
	cfg := loadConfig(home)

	if cfg.OpencodeDBPath != filepath.Join(home, ".local", "share", "opencode", "opencode.db") {
		t.Errorf("OpencodeDBPath = %q", cfg.OpencodeDBPath)
	}
	if cfg.ClaudeProjectsDir != filepath.Join(home, ".claude", "projects") {
		t.Errorf("ClaudeProjectsDir = %q", cfg.ClaudeProjectsDir)
	}
	if cfg.HumanReminderMinutes != 120 {
		t.Errorf("HumanReminderMinutes = %d, want 120", cfg.HumanReminderMinutes)
	}
	if !cfg.IngestEnabled {
		t.Error("IngestEnabled should default true")
	}
	if cfg.TickInterval != 60*time.Second {
		t.Errorf("TickInterval = %v, want 60s", cfg.TickInterval)
	}
}

func TestLoadConfigOverlayPartial(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".hh-tracker")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"idle_close_minutes":3}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig(home)
	if cfg.HumanReminderMinutes != 120 {
		t.Errorf("deprecated idle setting changed reminder: %d", cfg.HumanReminderMinutes)
	}
	if !cfg.IngestEnabled {
		t.Error("IngestEnabled should still default true")
	}
	if cfg.ClaudeProjectsDir != filepath.Join(home, ".claude", "projects") {
		t.Errorf("ClaudeProjectsDir should stay default, got %q", cfg.ClaudeProjectsDir)
	}
}

func TestLoadConfigMalformedFallsBack(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".hh-tracker")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{not json`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig(home)
	if cfg.HumanReminderMinutes != 120 || !cfg.IngestEnabled {
		t.Errorf("malformed config should yield defaults, got %+v", cfg)
	}
}

func TestLoadConfigReminderValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"configured", `{"human_reminder_minutes":90,"idle_close_minutes":1}`, 90},
		{"zero", `{"human_reminder_minutes":0}`, 120},
		{"negative", `{"human_reminder_minutes":-1}`, 120},
		{"overflow", `{"human_reminder_minutes":9223372036854775807}`, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".hh-tracker")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := loadConfig(home)
			if cfg.HumanReminderMinutes != tc.want {
				t.Fatalf("reminder=%d want %d", cfg.HumanReminderMinutes, tc.want)
			}
		})
	}
}
