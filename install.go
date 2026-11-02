package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type commandRunner func(name string, args ...string) ([]byte, error)

const trackerHookStatus = "HH Tracker: project context"

func runCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// mergeCodexHooks changes only our marked UserPromptSubmit handler. Other
// events, group fields and unknown JSON values remain semantically unchanged.
func mergeCodexHooks(raw []byte, executable string) ([]byte, error) {
	if !filepath.IsAbs(executable) {
		return nil, fmt.Errorf("hook executable must be absolute")
	}
	document := map[string]json.RawMessage{}
	if raw != nil {
		if err := validateJSONObject(raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			return nil, err
		}
	}
	events := map[string]json.RawMessage{}
	if block, ok := document["hooks"]; ok {
		if err := json.Unmarshal(block, &events); err != nil || events == nil {
			return nil, fmt.Errorf("hooks must be an object")
		}
	}
	// Validate nested hook collections before performing any CLI mutation.
	for _, eventRaw := range events {
		var groups []map[string]json.RawMessage
		if err := json.Unmarshal(eventRaw, &groups); err != nil || groups == nil {
			return nil, fmt.Errorf("hook event must be an array")
		}
		for _, group := range groups {
			var handlers []map[string]json.RawMessage
			if group == nil {
				return nil, fmt.Errorf("hook group must be an object")
			}
			if err := json.Unmarshal(group["hooks"], &handlers); err != nil || handlers == nil {
				return nil, fmt.Errorf("group hooks must be an array")
			}
			for _, handler := range handlers {
				if handler == nil {
					return nil, fmt.Errorf("hook handler must be an object")
				}
				var kind string
				if err := json.Unmarshal(handler["type"], &kind); err != nil || kind == "" {
					return nil, fmt.Errorf("hook handler type must be a string")
				}
				if kind == "command" {
					var command string
					if err := json.Unmarshal(handler["command"], &command); err != nil || command == "" {
						return nil, fmt.Errorf("command hook requires a string command")
					}
				}
			}
		}
	}
	var groups []map[string]json.RawMessage
	if eventRaw, ok := events["UserPromptSubmit"]; ok {
		json.Unmarshal(eventRaw, &groups)
	}
	owned := map[string]json.RawMessage{}
	setJSON := func(key string, value any) { owned[key], _ = json.Marshal(value) }
	setJSON("type", "command")
	setJSON("command", shellQuote(executable)+" hook codex")
	setJSON("timeout", 10)
	setJSON("statusMessage", trackerHookStatus)
	setJSON("additionalContextLimit", 1000)
	found := false
	for _, group := range groups {
		var handlers []map[string]json.RawMessage
		json.Unmarshal(group["hooks"], &handlers)
		kept := []map[string]json.RawMessage{}
		for _, handler := range handlers {
			var status string
			json.Unmarshal(handler["statusMessage"], &status)
			if status == trackerHookStatus {
				if found {
					continue
				}
				for key, value := range owned {
					handler[key] = value
				}
				found = true
			}
			kept = append(kept, handler)
		}
		group["hooks"], _ = json.Marshal(kept)
	}
	if !found {
		handlerRaw, _ := json.Marshal([]map[string]json.RawMessage{owned})
		groups = append(groups, map[string]json.RawMessage{"hooks": handlerRaw})
	}
	events["UserPromptSubmit"], _ = json.Marshal(groups)
	document["hooks"], _ = json.Marshal(events)
	result, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}

func installCodex(codexHome, executable string, run commandRunner) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("Codex hook installer requires a POSIX shell; configure hooks explicitly on Windows")
	}
	if !filepath.IsAbs(codexHome) {
		return fmt.Errorf("Codex home must be absolute")
	}
	path := filepath.Join(codexHome, "hooks.json")
	original, exists, mode, err := readInstallFile(path)
	if err != nil {
		return err
	}
	updated, err := mergeCodexHooks(original, executable)
	if err != nil {
		return fmt.Errorf("Codex hooks refused before MCP registration: %w", err)
	}
	if out, err := run("codex", "mcp", "add", "altiro-hhtracker", "--", executable); err != nil {
		return fmt.Errorf("Codex MCP registration failed; hooks unchanged: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Preflight and CLI registration are separate operations. Refuse to replace
	// a file changed in the meantime and report the already-completed MCP step.
	current, currentExists, _, err := readInstallFile(path)
	if err != nil || currentExists != exists || !bytes.Equal(current, original) {
		return fmt.Errorf("Codex MCP registered, hooks not installed: configuration changed during installation")
	}
	if bytes.Equal(original, updated) {
		return nil
	}
	if err := atomicInstallWrite(path, updated, mode); err != nil {
		return fmt.Errorf("Codex MCP registered, hooks not installed: %w", err)
	}
	return nil
}

func readInstallFile(path string) ([]byte, bool, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, 0600, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, 0, fmt.Errorf("refusing non-regular configuration file")
	}
	raw, err := os.ReadFile(path)
	return raw, true, info.Mode().Perm(), err
}

func atomicInstallWrite(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hh-tracker-config-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func installClaudeCode() error { return registerClaude(runCommand) }

func registerClaude(run commandRunner) error {
	// Preserve the existing user-scoped Claude registration protocol. Only our
	// named server is removed; unrelated definitions are not touched.
	run("claude", "mcp", "remove", "altiro-hhtracker", "--scope", "user")
	out, err := run("claude", "mcp", "add", "altiro-hhtracker", "--scope", "user", "--", "altiro-hhtracker")
	if err != nil {
		return fmt.Errorf("Claude registration failed after removing its prior owned entry: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func installOpenCode(cfgPath string) error {
	raw, exists, mode, err := readInstallFile(cfgPath)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("OpenCode configuration is missing")
	}
	if err := validateJSONObject(raw); err != nil {
		return fmt.Errorf("OpenCode configuration refused: %w", err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	block := map[string]json.RawMessage{}
	if value, ok := cfg["mcp"]; ok {
		if err := json.Unmarshal(value, &block); err != nil || block == nil {
			return fmt.Errorf("mcp must be an object")
		}
	}
	owned := map[string]json.RawMessage{}
	if value, ok := block["altiro-hhtracker"]; ok {
		if err := json.Unmarshal(value, &owned); err != nil || owned == nil {
			return fmt.Errorf("owned MCP entry must be an object")
		}
	}
	owned["type"], _ = json.Marshal("local")
	owned["command"], _ = json.Marshal("altiro-hhtracker")
	block["altiro-hhtracker"], _ = json.Marshal(owned)
	cfg["mcp"], _ = json.Marshal(block)
	updated, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicInstallWrite(cfgPath, append(updated, '\n'), mode)
}

func installClients(home, codexHome, executable string, lookup func(string) (string, error), run commandRunner, output io.Writer) error {
	succeeded, failed := 0, 0
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		if err := registerClaude(run); err != nil {
			fmt.Fprintf(output, "Claude Code: ERROR — %v\n", err)
			failed++
		} else {
			fmt.Fprintln(output, "Claude Code: MCP registered (instruction-driven tracking).")
			succeeded++
		}
	}
	opencode := filepath.Join(home, ".config", "opencode", "config.json")
	if _, err := os.Stat(opencode); err == nil {
		if err := installOpenCode(opencode); err != nil {
			fmt.Fprintf(output, "OpenCode: ERROR — %v\n", err)
			failed++
		} else {
			fmt.Fprintln(output, "OpenCode: MCP registered (instruction-driven tracking).")
			succeeded++
		}
	}
	if _, err := lookup("codex"); err == nil {
		if err := installCodex(codexHome, executable, run); err != nil {
			fmt.Fprintf(output, "Codex: ERROR — %v\n", err)
			failed++
		} else {
			fmt.Fprintln(output, "Codex: MCP registered and UserPromptSubmit hook installed. Review/trust it with /hooks; no trust settings were changed.")
			succeeded++
		}
	} else if _, err := os.Stat(filepath.Join(codexHome, "config.toml")); err == nil {
		fmt.Fprintln(output, "Codex: ERROR — CLI unavailable; no Codex settings were changed.")
		failed++
	}
	if failed > 0 {
		return fmt.Errorf("installation partial: %d clients configured, %d failed; successful changes remain", succeeded, failed)
	}
	if succeeded == 0 {
		return fmt.Errorf("no compatible client detected; configure an MCP server with command altiro-hhtracker")
	}
	fmt.Fprintln(output, "Restart your client to load MCP settings. Codex hooks also require explicit review/trust.")
	return nil
}

func installCmd() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if err := installClients(home, codexHome, executable, exec.LookPath, runCommand, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
