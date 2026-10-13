package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func hookEvent(cwd, session, turn string) string {
	body, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "cwd": cwd, "session_id": session, "turn_id": turn, "prompt": "private human prompt: never store or echo"})
	return string(body)
}

func hookContext(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("invalid output: %s", body)
	}
	return out.HookSpecificOutput.AdditionalContext
}

func TestCodexHookInjectsActionableUnconfirmedContext(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	var out bytes.Buffer
	if err := runCodexHook(strings.NewReader(hookEvent(cwd, "chat", "turn")), &out, home, time.Now()); err != nil {
		t.Fatal(err)
	}
	context := hookContext(t, out.Bytes())
	for _, required := range []string{"hh_tracking", "turn:human", "confirm", "root"} {
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
	var humanAt any
	var token string
	if err := db.QueryRow("SELECT last_human_at,claim_token FROM tracking_contexts").Scan(&humanAt, &token); err != nil {
		t.Fatal(err)
	}
	if humanAt != nil || token != "" {
		t.Fatal("ambiguous prompt advanced human time or claimed a question")
	}
	var event string
	db.QueryRow("SELECT event_id FROM tracking_events").Scan(&event)
	if event != "turn:observed" {
		t.Fatalf("unsafe dedupe namespace: %s", event)
	}
}

func TestCodexHookRefusesInvalidInputBeforeDB(t *testing.T) {
	for _, input := range []string{`{broken`, `null`, `[]`, `{} {}`, `{"hook_event_name":"UserPromptSubmit"}`, `{"hook_event_name":"UserPromptSubmit","session_id":2}`, strings.Repeat("x", maxHookInput+1)} {
		t.Run(fmt.Sprint(len(input)), func(t *testing.T) {
			home := t.TempDir()
			var out bytes.Buffer
			if err := runCodexHook(strings.NewReader(input), &out, home, time.Now()); err == nil {
				t.Fatal("invalid input accepted")
			}
			if out.Len() != 0 {
				t.Fatal("malformed input emitted developer context")
			}
			if _, err := os.Stat(filepath.Join(home, ".hh-tracker")); !os.IsNotExist(err) {
				t.Fatal("invalid input touched database")
			}
		})
	}
}

func TestCodexHookExcludesStopSyntheticAndSubagents(t *testing.T) {
	for _, extra := range []map[string]any{{"hook_event_name": "Stop"}, {"synthetic": true}, {"is_subagent": true}, {"agent_id": "child"}, {"source": "automation"}} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			home := t.TempDir()
			var event map[string]any
			json.Unmarshal([]byte(hookEvent(t.TempDir(), "chat", "turn")), &event)
			for key, value := range extra {
				event[key] = value
			}
			raw, _ := json.Marshal(event)
			var out bytes.Buffer
			if err := runCodexHook(bytes.NewReader(raw), &out, home, time.Now()); err != nil {
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

func TestCodexHookNeverTrustsUndocumentedHumanFlags(t *testing.T) {
	home := t.TempDir()
	var event map[string]any
	json.Unmarshal([]byte(hookEvent(t.TempDir(), "chat", "turn")), &event)
	event["confirmed_human"] = true
	event["root"] = true
	event["source"] = "human"
	raw, _ := json.Marshal(event)
	var out bytes.Buffer
	if err := runCodexHook(bytes.NewReader(raw), &out, home, time.Now()); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(filepath.Join(home, ".hh-tracker", "hh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	db.QueryRow("SELECT count(*) FROM tracking_contexts WHERE last_human_at IS NOT NULL OR claim_token<>''").Scan(&count)
	if count != 0 {
		t.Fatal("undocumented flags certified human provenance")
	}
}

func TestCodexHookReplayDoesNotSuppressRootHumanConfirmation(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := runCodexHook(strings.NewReader(hookEvent(cwd, "chat", "turn")), &out, home, now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	db, err := openDB(filepath.Join(home, ".hh-tracker", "hh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input := humanEvent("", "chat", "turn:human")
	input.Client = "codex"
	input.Cwd = cwd
	confirmed := trackAt(t, db, input, now)
	if !confirmed.Ask || confirmed.Replay || confirmed.Pending != "initialize" {
		t.Fatalf("technical event suppressed human confirmation: %+v", confirmed)
	}
	replay := trackAt(t, db, input, now.Add(time.Minute))
	if replay.Ask || !replay.Replay {
		t.Fatalf("human replay repeated prompt: %+v", replay)
	}
}

// A disposable compiled executable tests the public stdin adapter and real MCP
// transport. It never installs clients or reads a production HOME.
func TestCodexExecutableHookAndMCPWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled executable integration")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook command integration")
	}
	executable := filepath.Join(t.TempDir(), "tracker's binary")
	build := exec.Command("go", "build", "-o", executable, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build temporary executable: %v %s", err, output)
	}
	home := t.TempDir()
	cwd := t.TempDir()
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "CODEX_HOME=") {
			env = append(env, value)
		}
	}
	env = append(env, "HOME="+home, "CODEX_HOME="+filepath.Join(home, ".codex"))
	invokeHook := func(input string) ([]byte, error) {
		cmd := exec.Command("sh", "-c", shellQuote(executable)+" hook codex")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(input)
		var out bytes.Buffer
		cmd.Stdout = &out
		var diagnostic bytes.Buffer
		cmd.Stderr = &diagnostic
		err := cmd.Run()
		return out.Bytes(), err
	}
	observed, err := invokeHook(hookEvent(cwd, "chat-from-yesterday", "turn-one"))
	if err != nil {
		t.Fatal(err)
	}
	if context := hookContext(t, observed); !strings.Contains(context, "turn-one:human") || strings.Contains(context, "private human prompt") {
		t.Fatalf("bad executable context: %s", context)
	}
	for _, invalid := range []string{`{broken`, hookEvent(cwd, "chat", "trailing") + ` {}`, strings.Repeat("x", maxHookInput+1)} {
		output, err := invokeHook(invalid)
		if err == nil || len(output) != 0 {
			t.Fatalf("invalid executable input accepted: error=%v output=%s", err, output)
		}
	}
	call := func(name string, args any) string {
		t.Helper()
		initialize := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}}
		request := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
		var input bytes.Buffer
		json.NewEncoder(&input).Encode(initialize)
		json.NewEncoder(&input).Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		json.NewEncoder(&input).Encode(request)
		cmd := exec.Command(executable)
		cmd.Env = env
		cmd.Stdin = &input
		var out bytes.Buffer
		cmd.Stdout = &out
		var diagnostic bytes.Buffer
		cmd.Stderr = &diagnostic
		if err := cmd.Run(); err != nil {
			t.Fatalf("MCP process: %v %s", err, diagnostic.String())
		}
		scanner := bufio.NewScanner(&out)
		for scanner.Scan() {
			var response struct {
				ID     int             `json:"id"`
				Error  json.RawMessage `json:"error"`
				Result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID == 2 {
				if len(response.Error) != 0 && string(response.Error) != "null" {
					t.Fatalf("MCP error: %s", response.Error)
				}
				if len(response.Result.Content) != 1 {
					t.Fatalf("missing MCP content: %s", scanner.Bytes())
				}
				return response.Result.Content[0].Text
			}
		}
		t.Fatalf("missing MCP response: %s %s", out.String(), diagnostic.String())
		return ""
	}
	input := humanEvent("", "chat-from-yesterday", "turn-one:human")
	input.Client = "codex"
	input.Cwd = cwd
	var pending TrackingResult
	if err := json.Unmarshal([]byte(call("hh_tracking", input)), &pending); err != nil {
		t.Fatal(err)
	}
	if !pending.Ask || pending.Pending != "initialize" {
		t.Fatalf("root confirmation failed: %+v", pending)
	}
	input.Action = "initialize"
	input.Project = "project"
	input.EventID = "confirmed-project:human"
	input.ClaimToken = pending.ClaimToken
	var ready TrackingResult
	if err := json.Unmarshal([]byte(call("hh_tracking", input)), &ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Initialized || ready.Pending != "" {
		t.Fatalf("initialization failed: %+v", ready)
	}
	if text := call("hh_start", map[string]any{"descripcion": "implement function and verify", "proyecto": "project"}); strings.Contains(text, "ERROR") {
		t.Fatal(text)
	}
	observed, err = invokeHook(hookEvent(cwd, "another-chat", "new-task"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hookContext(t, observed), "initialized=true") {
		t.Fatal("new chat lost shared project context")
	}
	input = humanEvent("", "another-chat", "new-task:human")
	input.Client = "codex"
	input.Cwd = cwd
	var shared TrackingResult
	if err := json.Unmarshal([]byte(call("hh_tracking", input)), &shared); err != nil {
		t.Fatal(err)
	}
	if shared.Ask || !shared.Initialized || len(shared.TaskIDs) != 1 || shared.TaskIDs[0] != 1 {
		t.Fatalf("continued chat repeated question or lost task: %+v", shared)
	}
	if text := call("hh_close", map[string]any{"entry_id": 1, "resumen": "implemented and verified"}); strings.Contains(text, "ERROR") {
		t.Fatal(text)
	}
	db, err := openDB(filepath.Join(home, ".hh-tracker", "hh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entry, err := getEntry(db, 1)
	if err != nil || entry.Estado != "cerrado" {
		t.Fatalf("task not recorded: %+v %v", entry, err)
	}
	input.EventID = "after-gap:human"
	gap := trackAt(t, db, input, time.Now().Add(2*time.Hour+time.Minute))
	if !gap.Ask || gap.Pending != "continuity" {
		t.Fatalf("deterministic gap missed: %+v", gap)
	}
	input.Action = "resolve"
	input.EventID = "resolved-gap:human"
	input.ClaimToken = gap.ClaimToken
	resolved := trackAt(t, db, input, time.Now().Add(2*time.Hour+2*time.Minute))
	if resolved.Pending != "" {
		t.Fatalf("gap not resolved: %+v", resolved)
	}
	after, _ := getEntry(db, 1)
	if after != entry {
		t.Fatal("gap resolution changed historical task hours")
	}
	for _, excluded := range []string{`{"hook_event_name":"Stop"}`, `{"hook_event_name":"UserPromptSubmit","is_subagent":true}`, `{"hook_event_name":"UserPromptSubmit","synthetic":true}`} {
		output, err := invokeHook(excluded)
		if err != nil || strings.Contains(string(output), "additionalContext") {
			t.Fatalf("excluded executable event: %v %s", err, output)
		}
	}
	for _, file := range []string{"hh.db", "hh.db-wal"} {
		raw, _ := os.ReadFile(filepath.Join(home, ".hh-tracker", file))
		if bytes.Contains(raw, []byte("private human prompt")) {
			t.Fatal("prompt persisted")
		}
	}
}

func TestCodexHookLargePromptIsDiscardedAndSchemaValidated(t *testing.T) {
	home := t.TempDir()
	var event map[string]any
	json.Unmarshal([]byte(hookEvent(t.TempDir(), "chat", "turn")), &event)
	event["prompt"] = strings.Repeat("sensitive-content", 10000)
	raw, _ := json.Marshal(event)
	if len(raw) >= maxHookInput {
		t.Fatal("fixture exceeds limit")
	}
	var out bytes.Buffer
	if err := runCodexHook(bytes.NewReader(raw), &out, home, time.Now()); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 6000 || strings.Contains(out.String(), "sensitive-content") {
		t.Fatal("large prompt leaked or inflated model context")
	}
	for _, invalid := range []string{`{"hook_event_name":"UserPromptSubmit","hook_event_name":"Stop"}`, strings.Replace(hookEvent(t.TempDir(), "chat", "turn"), `"private human prompt: never store or echo"`, `null`, 1)} {
		fresh := t.TempDir()
		out.Reset()
		if err := runCodexHook(strings.NewReader(invalid), &out, fresh, time.Now()); err == nil {
			t.Fatal("ambiguous or malformed schema accepted")
		}
	}
}

func TestFirstCodexHookPreservesPythonHistory(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, ".claude", "mcp-servers", "hh-tracker", "hh.db")
	legacy, err := openDB(source)
	if err != nil {
		t.Fatal(err)
	}
	seedClosed(t, legacy, "legacy human work", "project", "2026-08-01T09:00:00", "2026-08-01T10:00:00", 1)
	before, _ := getEntry(legacy, 1)
	legacy.Close()
	var out bytes.Buffer
	if err := runCodexHook(strings.NewReader(hookEvent(t.TempDir(), "chat", "turn")), &out, home, time.Now()); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(filepath.Join(home, ".hh-tracker", "hh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := getEntry(db, 1)
	if err != nil || after != before {
		t.Fatalf("hook concealed old history: %+v %v", after, err)
	}
}
