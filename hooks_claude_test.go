package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeHookEvent follows Claude Code's UserPromptSubmit input: no turn_id.
func claudeHookEvent(cwd, session string) string {
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"cwd":             cwd,
		"session_id":      session,
		"transcript_path": "/home/x/.claude/projects/p/" + session + ".jsonl",
		"permission_mode": "default",
		"prompt":          "private human prompt: never store or echo",
	})
	return string(body)
}

func TestClaudeHookInjectsActionableUnconfirmedContext(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := runClaudeHook(strings.NewReader(claudeHookEvent(cwd, "chat-1")), &out, home, now); err != nil {
		t.Fatal(err)
	}
	context := hookContext(t, out.Bytes())
	turn := fmt.Sprintf("%d", now.UnixNano())
	for _, required := range []string{"hh_tracking", `"client":"claude"`, `"chat":"chat-1"`, turn + ":human", "confirm", "root", "own claim_token"} {
		if !strings.Contains(context, required) {
			t.Fatalf("missing %s: %s", required, context)
		}
	}
	if strings.Contains(context, "private human prompt") {
		t.Fatal("prompt leaked")
	}
	db, err := openDB(filepath.Join(home, ".hh-tracker", "hh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	db.QueryRow("SELECT count(*) FROM tracking_contexts WHERE last_human_at IS NOT NULL OR claim_token<>''").Scan(&count)
	if count != 0 {
		t.Fatal("the hook itself certified human provenance")
	}
}

func TestClaudeHookEachPromptGetsItsOwnEventID(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	first := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	var a, b bytes.Buffer
	if err := runClaudeHook(strings.NewReader(claudeHookEvent(cwd, "chat-1")), &a, home, first); err != nil {
		t.Fatal(err)
	}
	if err := runClaudeHook(strings.NewReader(claudeHookEvent(cwd, "chat-1")), &b, home, second); err != nil {
		t.Fatal(err)
	}
	if hookContext(t, a.Bytes()) == hookContext(t, b.Bytes()) {
		t.Fatal("two prompts shared the same event_id")
	}
}

func TestClaudeHookRefusesInvalidInputBeforeDB(t *testing.T) {
	for name, input := range map[string]string{
		"not json":        "nope",
		"missing session": `{"hook_event_name":"UserPromptSubmit","cwd":"/tmp","prompt":"x"}`,
		"relative cwd":    `{"hook_event_name":"UserPromptSubmit","cwd":"tmp","session_id":"s","prompt":"x"}`,
		"missing prompt":  `{"hook_event_name":"UserPromptSubmit","cwd":"/tmp","session_id":"s"}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			var out bytes.Buffer
			if err := runClaudeHook(strings.NewReader(input), &out, home, time.Now()); err == nil {
				t.Fatal("expected an error")
			}
			if _, err := os.Stat(filepath.Join(home, ".hh-tracker")); !os.IsNotExist(err) {
				t.Fatal("invalid input touched human tracking")
			}
		})
	}
}

func TestClaudeHookIgnoresOtherEventsAndSubagents(t *testing.T) {
	for _, extra := range []map[string]any{{"hook_event_name": "Stop"}, {"agent_id": "child"}, {"is_subagent": true}} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			home := t.TempDir()
			var event map[string]any
			json.Unmarshal([]byte(claudeHookEvent(t.TempDir(), "chat")), &event)
			for key, value := range extra {
				event[key] = value
			}
			raw, _ := json.Marshal(event)
			var out bytes.Buffer
			if err := runClaudeHook(bytes.NewReader(raw), &out, home, time.Now()); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "additionalContext") {
				t.Fatal("excluded event injected task instructions")
			}
			if _, err := os.Stat(filepath.Join(home, ".hh-tracker")); !os.IsNotExist(err) {
				t.Fatal("excluded event touched human tracking")
			}
		})
	}
}
