package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCodexInstallPreservesHooksAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	raw := []byte(`{"description":"existing","future":{"number":1234567890123456789},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-stop"}]}],"UserPromptSubmit":[{"custom":"keep","hooks":[{"type":"command","command":"other"}]}]}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	run := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	executable := filepath.Join(t.TempDir(), "tracker's binary")
	if err := installCodex(home, executable, run); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := installCodex(home, executable, run); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("reinstall duplicated or reformatted hooks")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	json.Compact(&compact, document["future"])
	if compact.String() != `{"number":1234567890123456789}` {
		t.Fatalf("unknown precision/field changed: %s", document["future"])
	}
	var hooks map[string]json.RawMessage
	json.Unmarshal(document["hooks"], &hooks)
	if !strings.Contains(string(hooks["Stop"]), "other-stop") || strings.Contains(string(hooks["Stop"]), "hh-tracker") {
		t.Fatalf("Stop changed: %s", hooks["Stop"])
	}
	var groups []struct {
		Custom string           `json:"custom"`
		Hooks  []map[string]any `json:"hooks"`
	}
	json.Unmarshal(hooks["UserPromptSubmit"], &groups)
	if len(groups) != 2 || groups[0].Custom != "keep" || groups[0].Hooks[0]["command"] != "other" {
		t.Fatalf("existing group changed: %+v", groups)
	}
	expected := []string{"codex", "mcp", "add", "altiro-hhtracker", "--", executable}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], expected) || !reflect.DeepEqual(calls[1], expected) {
		t.Fatalf("unsafe CLI arguments: %v", calls)
	}
	mode, _ := os.Stat(path)
	if mode.Mode().Perm() != 0600 {
		t.Fatalf("permissions changed: %v", mode.Mode())
	}
}

func TestCodexMalformedHooksRefusedBeforeRegistration(t *testing.T) {
	for _, raw := range []string{``, `{broken`, `null`, `[]`, `{"hooks":null}`, `{"hooks":{"UserPromptSubmit":{}}}`, `{"hooks":{"UserPromptSubmit":[{"hooks":null}]}}`, `{"hooks":{},"hooks":{}}`, `{"hooks":{"UserPromptSubmit":[{"hooks":[{}]}]}}`, `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":7}]}]}}`, `{} {}`} {
		t.Run(raw, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "hooks.json")
			os.WriteFile(path, []byte(raw), 0600)
			called := false
			err := installCodex(home, "/tmp/tracker", func(string, ...string) ([]byte, error) { called = true; return nil, nil })
			if err == nil || called {
				t.Fatalf("malformed config registered or accepted: %v called=%v", err, called)
			}
			after, _ := os.ReadFile(path)
			if string(after) != raw {
				t.Fatal("malformed file overwritten")
			}
		})
	}
}

func TestCodexRegistrationFailureDoesNotWriteHooks(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	raw := []byte(`{"hooks":{}}`)
	os.WriteFile(path, raw, 0600)
	err := installCodex(home, "/tmp/tracker", func(string, ...string) ([]byte, error) { return []byte("failure"), errors.New("failed") })
	if err == nil {
		t.Fatal("failure reported as success")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("hooks changed after CLI failure")
	}
}

func TestCodexOwnedHandlerUpdatePreservesUnknownFields(t *testing.T) {
	raw := []byte(`{"hooks":{"UserPromptSubmit":[{"matcher":"ignored","future":"keep","hooks":[{"type":"command","command":"old","statusMessage":"HH Tracker: project context","future":1234567890123456789},{"type":"command","command":"unrelated"}]}]}}`)
	output, err := mergeCodexHooks(raw, "/new/tracker")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "1234567890123456789") || !strings.Contains(string(output), "unrelated") || !strings.Contains(string(output), "ignored") || strings.Contains(string(output), `"command": "old"`) {
		t.Fatalf("owned merge lost unrelated fields: %s", output)
	}
	again, err := mergeCodexHooks(output, "/new/tracker")
	if err != nil || !bytes.Equal(output, again) {
		t.Fatalf("merge not idempotent: %v", err)
	}
}

func TestCodexConcurrentConfigChangeReportsPartial(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	os.WriteFile(path, []byte(`{"hooks":{}}`), 0600)
	changed := []byte(`{"description":"changed by human","hooks":{}}`)
	err := installCodex(home, "/tracker", func(string, ...string) ([]byte, error) { os.WriteFile(path, changed, 0600); return nil, nil })
	if err == nil || !strings.Contains(err.Error(), "MCP registered, hooks not installed") {
		t.Fatalf("missing partial outcome: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(changed, after) {
		t.Fatal("concurrent change overwritten")
	}
}

func TestInstallRefusesSymlinkAndPreservesOpenCode(t *testing.T) {
	home := t.TempDir()
	actual := filepath.Join(home, "actual.json")
	os.WriteFile(actual, []byte(`{"hooks":{}}`), 0600)
	if err := os.Symlink(actual, filepath.Join(home, "hooks.json")); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := installCodex(home, "/tracker", func(string, ...string) ([]byte, error) { called = true; return nil, nil }); err == nil || called {
		t.Fatal("symlink configuration mutated")
	}
	for _, raw := range []string{`null`, `[]`, `{"mcp":null}`, `{"mcp":[]}`, `{"mcp":{},"mcp":{}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, []byte(raw), 0600)
		if err := installOpenCode(path); err == nil {
			t.Fatalf("malformed OpenCode config accepted: %s", raw)
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("malformed OpenCode config overwritten")
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"future":1234567890123456789,"mcp":{"other":{"type":"local","command":"other"}}}`)
	os.WriteFile(path, raw, 0600)
	if err := installOpenCode(path); err != nil {
		t.Fatal(err)
	}
	output, _ := os.ReadFile(path)
	if !strings.Contains(string(output), "1234567890123456789") || !strings.Contains(string(output), `"other"`) {
		t.Fatalf("OpenCode fields lost: %s", output)
	}
	first := append([]byte{}, output...)
	installOpenCode(path)
	output, _ = os.ReadFile(path)
	if !bytes.Equal(first, output) {
		t.Fatal("OpenCode reinstall not idempotent")
	}
}

func TestInstallClientsReportsPartialAndKeepsLegacyMCP(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{}`), 0600)
	codeDir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(codeDir, 0700)
	os.WriteFile(filepath.Join(codeDir, "config.json"), []byte(`{"mcp":{"other":{"command":"other"}}}`), 0600)
	codexHome := filepath.Join(home, ".codex")
	os.MkdirAll(codexHome, 0700)
	os.WriteFile(filepath.Join(codexHome, "hooks.json"), []byte(`{broken`), 0600)
	var calls [][]string
	run := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	var output bytes.Buffer
	err := installClients(home, codexHome, "/tracker", func(string) (string, error) { return "/codex", nil }, run, &output)
	if err == nil || !strings.Contains(err.Error(), "2 clients configured, 1 failed") || !strings.Contains(output.String(), "refused before MCP") {
		t.Fatalf("partial install misleading: %v %s", err, output.String())
	}
	expected := [][]string{{"claude", "mcp", "remove", "altiro-hhtracker", "--scope", "user"}, {"claude", "mcp", "add", "altiro-hhtracker", "--scope", "user", "--", "altiro-hhtracker"}}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("legacy command changed or Codex mutated: %v", calls)
	}
}

func TestCodexReinstallDetectsConcurrentRemoval(t *testing.T) {
	home := t.TempDir()
	noop := func(string, ...string) ([]byte, error) { return nil, nil }
	if err := installCodex(home, "/tracker", noop); err != nil {
		t.Fatal(err)
	}
	changed := []byte(`{"hooks":{}}`)
	err := installCodex(home, "/tracker", func(string, ...string) ([]byte, error) {
		os.WriteFile(filepath.Join(home, "hooks.json"), changed, 0600)
		return nil, nil
	})
	if err == nil {
		t.Fatal("reported hook installed after concurrent removal")
	}
	after, _ := os.ReadFile(filepath.Join(home, "hooks.json"))
	if !bytes.Equal(after, changed) {
		t.Fatal("concurrent removal overwritten")
	}
}
