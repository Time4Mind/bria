package main

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Time4Mind/bria/internal/config"
)

type backendCommands struct {
	Claude string
	Codex  string
}

func prepareManagedBackendCommands(
	nodeConfig config.Config,
	backendRuntime backendRuntime,
) (config.Config, map[string]string) {
	roots := map[string]string{
		"claude": filepath.Join(backendRuntime.home, ".bria", "providers", "claude"),
		"codex":  filepath.Join(backendRuntime.home, ".bria", "providers", "codex"),
	}
	if strings.TrimSpace(nodeConfig.ClaudeCommand) == "claude" {
		if _, err := backendRuntime.runner.LookPath("claude"); err != nil {
			nodeConfig.ClaudeCommand = managedBackendCommand(roots["claude"], "claude")
		}
	}
	if strings.TrimSpace(nodeConfig.CodexCommand) == "codex" {
		if _, err := backendRuntime.runner.LookPath("codex"); err != nil {
			nodeConfig.CodexCommand = managedBackendCommand(roots["codex"], "codex")
		}
	}
	return nodeConfig, roots
}

func configuredBackendCommands(nodeConfig config.Config) backendCommands {
	return backendCommands{Claude: nodeConfig.ClaudeCommand, Codex: nodeConfig.CodexCommand}
}

func managedBackendCommand(root, executable string) string {
	if runtime.GOOS == "windows" {
		executable += ".cmd"
	}
	return filepath.Join(root, "node_modules", ".bin", executable)
}
