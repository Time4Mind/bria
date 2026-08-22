package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
)

func TestTrustedStartupReconcilesStableProviderHooks(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "opt", "bria", "current", "bria")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(codex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codex, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"ccbot hook","timeout":5},{"type":"command","command":"/old/releases/a/bria provider-hook --config /var/bria.json","timeout":5}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	originalHome := providerHookHome
	providerHookHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { providerHookHome = originalHome })
	t.Setenv(providerHookActivationEnv, binary)
	reconcileTrustedProviderHooks(config.Config{}, "/var/bria.json")
	data, err := os.ReadFile(codex)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if strings.Contains(encoded, "/old/releases/a/bria") ||
		strings.Count(encoded, binary) != 3 || strings.Count(encoded, "ccbot hook") != 1 {
		t.Fatalf("startup hook reconciliation=%s", encoded)
	}
}

func TestTrustedStartupNeverTouchesIsolatedRunnerHome(t *testing.T) {
	originalHome := providerHookHome
	providerHookHome = func() (string, error) {
		t.Fatal("isolated control process resolved provider home")
		return "", nil
	}
	t.Cleanup(func() { providerHookHome = originalHome })
	reconcileTrustedProviderHooks(config.Config{Runner: config.RunnerConfig{
		Mode: config.RunnerModeNativeUser,
	}}, "/var/bria.json")
}
