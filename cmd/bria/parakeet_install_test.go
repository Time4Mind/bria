package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/config"
	"bria/internal/parakeetinstall"
)

func TestInstallParakeetUsesExactVersionedConfigPaths(t *testing.T) {
	directory := t.TempDir()
	configPath := writeVersionedRoleConfig(t, directory, config.RoleExecutor)
	var got parakeetinstall.Paths
	dependencies := commandDependencies{
		installSpeech: func(_ context.Context, paths parakeetinstall.Paths) error {
			got = paths
			return nil
		},
	}
	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"install-parakeet", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if got.Executable != filepath.Join(directory, "parakeet") || got.Model != filepath.Join(directory, "parakeet-model.bin") {
		t.Fatalf("installer paths = %#v", got)
	}
	if stdout.String() != "Parakeet dependencies: OK\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestInstallParakeetDoesNothingOnCoordinator(t *testing.T) {
	directory := t.TempDir()
	configPath := writeVersionedRoleConfig(t, directory, config.RoleCoordinator)
	dependencies := commandDependencies{
		installSpeech: func(context.Context, parakeetinstall.Paths) error {
			t.Fatal("coordinator attempted to install executor dependencies")
			return nil
		},
	}
	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"install-parakeet", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
}

func TestCheckConfigVerifiesExactParakeetPathsBeforeComposition(t *testing.T) {
	directory := t.TempDir()
	configPath := writeVersionedRoleConfig(t, directory, config.RoleExecutor)
	dependencies := testCommandDependencies(t, nil)
	var got parakeetinstall.Paths
	dependencies.verifySpeech = func(_ context.Context, paths parakeetinstall.Paths) error {
		got = paths
		return nil
	}
	if err := checkConfig(configPath, dependencies); err != nil {
		t.Fatal(err)
	}
	if got.Executable != filepath.Join(directory, "parakeet") || got.Model != filepath.Join(directory, "parakeet-model.bin") {
		t.Fatalf("verified paths = %#v", got)
	}
}
