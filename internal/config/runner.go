package config

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	RunnerModeTrusted    = "trusted"
	RunnerModeDocker     = "docker"
	RunnerModeNativeUser = "native-user"
	RunnerModeWSL        = "wsl"
)

// RunnerConfig separates provider CLIs and their workspaces from the Bria
// control plane. An omitted runner keeps the legacy, trusted in-process host
// behavior for Android, macOS, Windows, and existing installations.
type RunnerConfig struct {
	Mode   string `json:"mode,omitempty"`
	Socket string `json:"socket,omitempty"`
	Home   string `json:"home,omitempty"`
}

func (c Config) EffectiveRunnerMode() string {
	mode := strings.ToLower(strings.TrimSpace(c.Runner.Mode))
	if mode == "" {
		return RunnerModeTrusted
	}
	return mode
}

func (c Config) IsolatedRunner() bool {
	return c.EffectiveRunnerMode() != RunnerModeTrusted
}

func (c Config) validateRunner() error {
	mode := c.EffectiveRunnerMode()
	switch mode {
	case RunnerModeTrusted:
		if c.Runner.Socket != "" || c.Runner.Home != "" {
			return errors.New("runner.socket and runner.home are only allowed for an isolated runner")
		}
		return nil
	case RunnerModeDocker, RunnerModeNativeUser, RunnerModeWSL:
	default:
		return errors.New("runner.mode must be trusted, docker, native-user, or wsl")
	}
	if !filepath.IsAbs(c.Runner.Socket) {
		return errors.New("runner.socket must be absolute for an isolated runner")
	}
	if !filepath.IsAbs(c.Runner.Home) {
		return errors.New("runner.home must be absolute for an isolated runner")
	}
	if pathContains(c.DataDir, c.Runner.Socket) {
		return errors.New("runner.socket must be outside data_dir")
	}
	if pathContains(c.Runner.Home, c.DataDir) || pathContains(c.DataDir, c.Runner.Home) {
		return errors.New("runner.home and data_dir must not contain one another")
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (c *Config) expandRunnerPaths() {
	if c.Runner.Socket != "" {
		c.Runner.Socket = filepath.Clean(c.Runner.Socket)
	}
	if c.Runner.Home != "" {
		c.Runner.Home = filepath.Clean(c.Runner.Home)
	}
}
