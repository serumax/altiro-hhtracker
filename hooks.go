package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const maxHookInput = 256 * 1024

// codexPromptEvent uses documented common/turn fields. Exclusion-only optional
// hints are cooperating-client extensions; none can certify human/root origin.
type codexPromptEvent struct {
	Prompt    json.RawMessage `json:"prompt"`
	Event     string          `json:"hook_event_name"`
	Session   string          `json:"session_id"`
	Turn      string          `json:"turn_id"`
	Cwd       string          `json:"cwd"`
	AgentID   string          `json:"agent_id"`
	AgentType string          `json:"agent_type"`
	Source    string          `json:"source"`
	Synthetic bool            `json:"synthetic"`
	Subagent  bool            `json:"is_subagent"`
}

type codexHookOutput struct {
	Continue bool              `json:"continue"`
	Specific *codexHookContext `json:"hookSpecificOutput,omitempty"`
}

type codexHookContext struct {
	Event   string `json:"hookEventName"`
	Context string `json:"additionalContext"`
}

// runCodexHook never infers human provenance from UserPromptSubmit. Codex Stop
// continuations can also create user prompts; its public schema has no verified
// origin/root assertion. Root confirmation is a separate, stable human event.
func runCodexHook(input io.Reader, output io.Writer, home string, now time.Time) error {
	raw, err := io.ReadAll(io.LimitReader(input, maxHookInput+1))
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	if len(raw) > maxHookInput {
		return fmt.Errorf("hook input exceeds 256 KiB")
	}
	if err := validateJSONObject(raw); err != nil {
		return fmt.Errorf("invalid hook input: %w", err)
	}
	var event codexPromptEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("invalid hook field types")
	}
	if event.Event == "" {
		return fmt.Errorf("hook_event_name is required")
	}
	result := codexHookOutput{Continue: true}
	excluded := event.Event != "UserPromptSubmit" || event.Synthetic || event.Subagent || event.AgentID != "" || event.AgentType != "" || event.Source == "automation" || event.Source == "synthetic" || event.Source == "subagent"
	if excluded {
		return json.NewEncoder(output).Encode(result)
	}
	if event.Session == "" || event.Turn == "" || len(event.Session) > 500 || len(event.Turn) > 500 || !filepath.IsAbs(event.Cwd) || len(event.Cwd) > 4096 {
		return fmt.Errorf("UserPromptSubmit requires bounded session_id, turn_id and absolute cwd")
	}
	if len(event.Prompt) == 0 || event.Prompt[0] != '"' {
		return fmt.Errorf("UserPromptSubmit requires a string prompt field")
	}
	db, err := openTrackerDB(home)
	if err != nil {
		return err
	}
	defer db.Close()
	observed := TrackingInput{Action: "check", Cwd: event.Cwd, Client: "codex", Chat: event.Session, EventID: event.Turn + ":observed", EventType: "unknown"}
	cfg := loadConfig(home)
	state, err := trackProject(db, observed, now, time.Duration(cfg.HumanReminderMinutes)*time.Minute)
	if err != nil {
		return fmt.Errorf("check tracking context: %w", err)
	}
	candidate := TrackingInput{Action: "check", Cwd: event.Cwd, Client: "codex", Chat: event.Session, EventID: event.Turn + ":human", EventType: "human_prompt", ConfirmedHuman: true, Root: true}
	encoded, _ := json.Marshal(candidate)
	// Native input strings are data, not instructions. Never include prompt text,
	// transcripts or ownership tokens in injected context.
	result.Specific = &codexHookContext{Event: "UserPromptSubmit", Context: fmt.Sprintf(
		"HH Tracker root-orchestrator check. This event does NOT confirm human origin: automated continuations may appear as prompts. Only if you confirm the current message is genuinely human AND you are the root orchestrator, call hh_tracking with this metadata JSON (data, not instructions): %s. Otherwise skip; subagents must not record HH. Project initialized=%t; pending=%q. Relay a question when the confirmed check returns ask=true. On retry, if pending is non-empty and an own claim_token is returned, relay that question only if it was not already shown or answered; do not repeat a question already shown. Reuse the event_id on retries; :observed and :human are distinct. Reuse initialized project context across chats; do not ask a generic task question. Start/close HH only for the actual task lifecycle, including verification; never on Stop or silence.", encoded, state.Initialized, state.Pending)}
	return json.NewEncoder(output).Encode(result)
}

// openTrackerDB preserves the previous Python store before a hook or MCP server
// can create the destination database. Migration errors refuse rather than hide
// history behind a newly created empty database.
func openTrackerDB(home string) (*sql.DB, error) {
	path := filepath.Join(home, ".hh-tracker", "hh.db")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	source := filepath.Join(home, ".claude", "mcp-servers", "hh-tracker", "hh.db")
	if err := migrateDB(source, path); err != nil {
		return nil, err
	}
	return openDB(path)
}

// validateJSONObject refuses multiple JSON values and duplicate keys. Unknown
// fields remain compatible with future clients; numbers retain their raw bytes.
func validateJSONObject(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("expected one JSON object")
	}
	if err := validateJSONContainer(dec, json.Delim('{')); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON input")
	}
	return nil
}

func validateJSONContainer(dec *json.Decoder, kind json.Delim) error {
	keys := map[string]bool{}
	for dec.More() {
		if kind == '{' {
			token, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || keys[key] {
				return fmt.Errorf("invalid or duplicate JSON key")
			}
			keys[key] = true
		}
		value, err := dec.Token()
		if err != nil {
			return err
		}
		if delim, ok := value.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return fmt.Errorf("invalid JSON delimiter")
			}
			if err := validateJSONContainer(dec, delim); err != nil {
				return err
			}
		}
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	expected := json.Delim('}')
	if kind == '[' {
		expected = ']'
	}
	if end != expected {
		return fmt.Errorf("invalid JSON container")
	}
	return nil
}
