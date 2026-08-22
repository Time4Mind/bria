package providerbinding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var codexHookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop"}
var claudeHookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "StopFailure"}

// InstallReport contains bounded reconciliation diagnostics. It deliberately
// excludes hook commands and filesystem paths because both can disclose local
// account details.
type InstallReport struct {
	Changed    bool
	Migrations int
}

type hookPlan struct {
	path       string
	encoded    []byte
	changed    bool
	migrations int
}

func InstallHook(binary, configPath, hooksPath string) error {
	_, err := installHook(binary, configPath, hooksPath, "codex", codexHookEvents)
	return err
}

// InstallHooks validates the stable activation binary and both provider
// documents before replacing either file. A malformed second document cannot
// therefore leave only one provider migrated.
func InstallHooks(binary, configPath, codexPath, claudePath string) error {
	_, err := ReconcileHooks(binary, configPath, codexPath, claudePath)
	return err
}

func ReconcileHooks(binary, configPath, codexPath, claudePath string) (InstallReport, error) {
	if err := ValidateStableBinary(binary); err != nil {
		return InstallReport{}, err
	}
	if !filepath.IsAbs(configPath) {
		return InstallReport{}, errors.New("Bria config path must be absolute")
	}
	command := shellQuote(binary) + " provider-hook --config " + shellQuote(configPath)
	return reconcileHooks(command, codexPath, claudePath)
}

// ReconcileRunnerHooks installs hooks owned by an isolated provider account.
// The hook writes only to the runner-owned binding store; final delivery still
// has the bounded transcript watchdog when the control config is unavailable.
func ReconcileRunnerHooks(binary, bindingStore, codexPath, claudePath string) (InstallReport, error) {
	if err := ValidateStableBinary(binary); err != nil {
		return InstallReport{}, err
	}
	if !filepath.IsAbs(bindingStore) {
		return InstallReport{}, errors.New("provider binding store path must be absolute")
	}
	command := shellQuote(binary) + " provider-hook --binding-store " + shellQuote(bindingStore)
	return reconcileHooks(command, codexPath, claudePath)
}

func reconcileHooks(command, codexPath, claudePath string) (InstallReport, error) {
	plans := make([]hookPlan, 0, 2)
	for _, provider := range []struct {
		path    string
		backend string
		events  []string
	}{
		{codexPath, "codex", codexHookEvents},
		{claudePath, "claude", claudeHookEvents},
	} {
		plan, err := planHook(command, provider.path, provider.backend, provider.events)
		if err != nil {
			return InstallReport{}, err
		}
		plans = append(plans, plan)
	}
	report := InstallReport{}
	for _, plan := range plans {
		if !plan.changed {
			continue
		}
		if err := writeHookAtomic(plan.path, plan.encoded); err != nil {
			return report, err
		}
		report.Changed = true
		report.Migrations += plan.migrations
	}
	return report, nil
}

// ValidateStableBinary deliberately validates the lexical path. A current
// symlink may resolve below releases, but the serialized command must keep the
// stable path and must never capture a retention-managed release directly.
func ValidateStableBinary(binary string) error {
	if binary == "" || !filepath.IsAbs(binary) || filepath.Clean(binary) != binary ||
		strings.ContainsAny(binary, "\r\n\x00") {
		return errors.New("installed Bria hook binary must be a clean absolute path")
	}
	parts := strings.FieldsFunc(filepath.ToSlash(binary), func(r rune) bool { return r == '/' })
	for index, part := range parts {
		if part == "releases" && index+1 < len(parts) {
			return errors.New("installed Bria hook binary must use the stable activation path, not releases/*")
		}
	}
	info, err := os.Stat(binary)
	if err != nil {
		return errors.New("installed Bria hook binary is unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("installed Bria hook binary must be an executable regular file")
	}
	return nil
}

func installHook(binary, configPath, hooksPath, backend string, events []string) (InstallReport, error) {
	if err := ValidateStableBinary(binary); err != nil {
		return InstallReport{}, err
	}
	command := shellQuote(binary) + " provider-hook --config " + shellQuote(configPath)
	plan, err := planHook(command, hooksPath, backend, events)
	if err != nil {
		return InstallReport{}, err
	}
	if !plan.changed {
		return InstallReport{}, nil
	}
	if err := writeHookAtomic(plan.path, plan.encoded); err != nil {
		return InstallReport{}, err
	}
	return InstallReport{Changed: true, Migrations: plan.migrations}, nil
}

func planHook(baseCommand, hooksPath, backend string, events []string) (hookPlan, error) {
	if !filepath.IsAbs(hooksPath) {
		return hookPlan{}, errors.New("provider hooks path must be absolute")
	}
	document := make(map[string]any)
	var existing []byte
	modeOK := false
	if info, err := os.Lstat(hooksPath); err == nil {
		if !info.Mode().IsRegular() {
			return hookPlan{}, errors.New("provider hooks path must be a regular file")
		}
		modeOK = info.Mode().Perm() == 0o600
		data, readErr := os.ReadFile(hooksPath)
		if readErr != nil {
			return hookPlan{}, fmt.Errorf("read provider hooks: %w", readErr)
		}
		existing = data
		if err := json.Unmarshal(data, &document); err != nil {
			return hookPlan{}, fmt.Errorf("decode provider hooks: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return hookPlan{}, fmt.Errorf("inspect provider hooks: %w", err)
	}
	hooks, ok := document["hooks"].(map[string]any)
	if !ok {
		if document["hooks"] != nil {
			return hookPlan{}, errors.New("provider hooks field is invalid")
		}
		hooks = make(map[string]any)
		document["hooks"] = hooks
	}
	command := baseCommand
	if backend != "codex" {
		command += " --backend " + shellQuote(backend)
	}
	migrations := 0
	for _, event := range events {
		entries, ok := hooks[event].([]any)
		if !ok && hooks[event] != nil {
			return hookPlan{}, fmt.Errorf("provider %s hooks are invalid", event)
		}
		var removed int
		entries, removed = removeBriaProviderCommands(entries, backend, command)
		migrations += removed
		entries = append(entries, map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": command, "timeout": float64(5),
		}}})
		hooks[event] = entries
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return hookPlan{}, fmt.Errorf("encode provider hooks: %w", err)
	}
	encoded = append(encoded, '\n')
	return hookPlan{
		path: hooksPath, encoded: encoded,
		changed: !modeOK || !bytes.Equal(existing, encoded), migrations: migrations,
	}, nil
}

func writeHookAtomic(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".hooks-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// removeBriaProviderCommands makes a single installer invocation the owner of
// the selected provider backend. It removes current and stale Bria environments
// while preserving hooks owned by other tools and providers.
func removeBriaProviderCommands(entries []any, backend, desiredCommand string) ([]any, int) {
	result := make([]any, 0, len(entries))
	removed := 0
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
			if isBriaProviderCommand(command, backend) {
				if command != desiredCommand {
					removed++
				}
				continue
			}
			filtered = append(filtered, rawHook)
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
	return result, removed
}

func isBriaProviderCommand(command, backend string) bool {
	markers := []string{" provider-hook --config ", " provider-hook --binding-store "}
	position := -1
	marker := ""
	for _, candidate := range markers {
		if found := strings.Index(command, candidate); found > 0 {
			position = found
			marker = candidate
			break
		}
	}
	if position <= 0 {
		return false
	}
	binary := strings.Trim(strings.TrimSpace(command[:position]), "'\"")
	if filepath.Base(binary) != "bria" {
		return false
	}
	remainder := strings.TrimSpace(command[position+len(marker):])
	if backend == "codex" {
		return remainder != "" && !strings.Contains(remainder, " --backend ")
	}
	return strings.HasSuffix(remainder, " --backend "+shellQuote(backend))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
