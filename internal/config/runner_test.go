package config_test

import (
	"path/filepath"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
)

func TestRunnerIsolationRequiresSeparatedAbsolutePaths(t *testing.T) {
	configured := validConfig(t)
	configured.Runner = config.RunnerConfig{
		Mode:   config.RunnerModeNativeUser,
		Socket: "/run/bria-runner/runner.sock",
		Home:   "/srv/bria-agent",
	}
	if err := configured.Validate(); err != nil {
		t.Fatalf("valid isolated runner rejected: %v", err)
	}

	for name, mutate := range map[string]func(*config.Config){
		"relative socket": func(candidate *config.Config) { candidate.Runner.Socket = "runner.sock" },
		"relative home":   func(candidate *config.Config) { candidate.Runner.Home = "agent" },
		"socket below control data": func(candidate *config.Config) {
			candidate.Runner.Socket = filepath.Join(candidate.DataDir, "runner.sock")
		},
		"control data below runner home": func(candidate *config.Config) {
			candidate.Runner.Home = filepath.Dir(candidate.DataDir)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := configured
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("unsafe runner configuration accepted")
			}
		})
	}
}

func TestRunnerDefaultsToTrustedForExistingConfigs(t *testing.T) {
	configured := validConfig(t)
	if configured.EffectiveRunnerMode() != config.RunnerModeTrusted || configured.IsolatedRunner() {
		t.Fatalf("unexpected default runner: %#v", configured.Runner)
	}
	configured.Runner.Socket = "/run/runner.sock"
	if err := configured.Validate(); err == nil {
		t.Fatal("trusted runner accepted an execution socket")
	}
}
