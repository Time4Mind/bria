package main

import (
	"os"
	"path/filepath"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/providerbinding"
)

var providerHookHome = os.UserHomeDir

const providerHookActivationEnv = "BRIA_PROVIDER_HOOK_ACTIVATION"

func providerHookActivationPath() string {
	if configured := os.Getenv(providerHookActivationEnv); configured != "" {
		return configured
	}
	resolved, _ := resolveActivationPath()
	return resolved
}

// reconcileTrustedProviderHooks runs only where Bria and provider CLIs share
// one account. An isolated runner owns its home; the privileged control
// process must never rewrite that other account's provider settings.
func reconcileTrustedProviderHooks(nodeConfig config.Config, configPath string) {
	if nodeConfig.IsolatedRunner() {
		return
	}
	binary := providerHookActivationPath()
	if binary == "" {
		processlog.Failuref(
			processlog.Service, processlog.FailureInvalidState,
			"bria provider_hooks: outcome=rejected owner=trusted reason=activation_path_unavailable",
		)
		return
	}
	home, err := providerHookHome()
	if err != nil {
		processlog.Failuref(
			processlog.Service, processlog.FailureInvalidState,
			"bria provider_hooks: outcome=rejected owner=trusted reason=home_unavailable",
		)
		return
	}
	report, err := providerbinding.ReconcileHooks(
		binary, configPath,
		filepath.Join(home, ".codex", "hooks.json"),
		filepath.Join(home, ".claude", "settings.json"),
	)
	if err != nil {
		processlog.Failuref(
			processlog.Service, processlog.FailureInvalidState,
			"bria provider_hooks: outcome=rejected owner=trusted path_class=stable_candidate",
		)
		return
	}
	if report.Changed {
		processlog.Servicef(
			"bria provider_hooks: outcome=reconciled owner=trusted path_class=stable migrations=%d",
			report.Migrations,
		)
	}
}
