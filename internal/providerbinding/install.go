package providerbinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var codexHookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop"}
var claudeHookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "StopFailure"}

func InstallHook(binary, configPath, hooksPath string) error {
	return installHook(binary, configPath, hooksPath, "codex", codexHookEvents)
}

func InstallHooks(binary, configPath, codexPath, claudePath string) error {
	if err := installHook(binary, configPath, codexPath, "codex", codexHookEvents); err != nil {
		return err
	}
	return installHook(binary, configPath, claudePath, "claude", claudeHookEvents)
}

func installHook(binary, configPath, hooksPath, backend string, events []string) error {
	for label, path := range map[string]string{
		"Bria binary": binary, "Bria config": configPath, "Codex hooks": hooksPath,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute", label)
		}
	}
	document := make(map[string]any)
	if info, err := os.Lstat(hooksPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Codex hooks path must be a regular file")
		}
		data, readErr := os.ReadFile(hooksPath)
		if readErr != nil {
			return fmt.Errorf("read Codex hooks: %w", readErr)
		}
		if err := json.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("decode Codex hooks: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Codex hooks: %w", err)
	}
	hooks, ok := document["hooks"].(map[string]any)
	if !ok {
		if document["hooks"] != nil {
			return errors.New("Codex hooks field is invalid")
		}
		hooks = make(map[string]any)
		document["hooks"] = hooks
	}
	command := shellQuote(binary) + " provider-hook --config " + shellQuote(configPath)
	if backend != "codex" {
		command += " --backend " + shellQuote(backend)
	}
	for _, event := range events {
		entries, ok := hooks[event].([]any)
		if !ok && hooks[event] != nil {
			return fmt.Errorf("Codex %s hooks are invalid", event)
		}
		entries = removeBriaProviderCommands(entries, configPath, backend)
		entries = append(entries, map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": command, "timeout": float64(5),
		}}})
		hooks[event] = entries
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Codex hooks: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(hooksPath), ".hooks-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, hooksPath)
}

// removeBriaProviderCommands makes a single installer invocation the owner of
// the selected Bria environment. It removes current and stale Bria binaries
// for the same config/backend while preserving other environments and tools.
func removeBriaProviderCommands(entries []any, configPath, backend string) []any {
	result := make([]any, 0, len(entries))
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			result = append(result, rawEntry)
			continue
		}
		inner, ok := entry["hooks"].([]any)
		if !ok {
			result = append(result, rawEntry)
			continue
		}
		filtered := make([]any, 0, len(inner))
		for _, rawHook := range inner {
			hook, _ := rawHook.(map[string]any)
			command, _ := hook["command"].(string)
			if !isBriaProviderCommand(command, configPath, backend) {
				filtered = append(filtered, rawHook)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		copyEntry := make(map[string]any, len(entry))
		for key, value := range entry {
			copyEntry[key] = value
		}
		copyEntry["hooks"] = filtered
		result = append(result, copyEntry)
	}
	return result
}

func isBriaProviderCommand(command, configPath, backend string) bool {
	marker := " provider-hook --config " + shellQuote(configPath)
	position := strings.Index(command, marker)
	if position <= 0 {
		return false
	}
	binary := strings.Trim(strings.TrimSpace(command[:position]), "'\"")
	if filepath.Base(binary) != "bria" {
		return false
	}
	remainder := strings.TrimSpace(command[position+len(marker):])
	if backend == "codex" {
		return remainder == ""
	}
	return remainder == "--backend "+shellQuote(backend)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
