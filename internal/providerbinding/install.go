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
		if !containsCommand(entries, command) {
			entries = append(entries, map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": command, "timeout": float64(5),
			}}})
		}
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

func containsCommand(entries []any, command string) bool {
	for _, rawEntry := range entries {
		entry, _ := rawEntry.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, rawHook := range inner {
			hook, _ := rawHook.(map[string]any)
			if hook["command"] == command {
				return true
			}
		}
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
