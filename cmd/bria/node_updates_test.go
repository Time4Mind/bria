package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/config"
)

func TestLocalReleaseCleanerExistsWithoutUpdateManifest(t *testing.T) {
	cleaner, err := newLocalReleaseCleaner(config.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cleaner.installRoot == "" || cleaner.activationPath == "" || cleaner.runningPath == "" {
		t.Fatalf("cleaner paths = %#v", cleaner)
	}
	if _, err := cleaner.CleanupArtifacts(time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementPreservesStableProviderHookActivation(t *testing.T) {
	t.Setenv(expectedBinarySHA256Env, "stale")
	t.Setenv(providerHookActivationEnv, "/old/current/bria")
	environment := environmentWithBinaryIdentity("fresh", "/opt/bria/current/bria")
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	if strings.Count(joined, "\n"+expectedBinarySHA256Env+"=") != 1 ||
		!strings.Contains(joined, "\n"+expectedBinarySHA256Env+"=fresh\n") {
		t.Fatalf("binary identity environment=%q", joined)
	}
	if strings.Count(joined, "\n"+providerHookActivationEnv+"=") != 1 ||
		!strings.Contains(joined, "\n"+providerHookActivationEnv+"=/opt/bria/current/bria\n") {
		t.Fatalf("provider activation environment=%q", joined)
	}
	t.Setenv(providerHookActivationEnv, "/stable/current/bria")
	if got := providerHookActivationPath(); got != "/stable/current/bria" {
		t.Fatalf("provider hook activation=%q", got)
	}
}

func TestPreflightUpdateCandidateAcceptsCurrentConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	binary := writePreflightFixture(t, "exit 0")
	if err := preflightUpdateCandidate(
		context.Background(), binary, filepath.Join(t.TempDir(), "config.json"),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightUpdateCandidateRejectsIncompatibleConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	binary := writePreflightFixture(t, `echo 'bria node: decode config: json: unknown field "runner"' >&2
exit 1`)
	err := preflightUpdateCandidate(
		context.Background(), binary, filepath.Join(t.TempDir(), "config.json"),
	)
	if err == nil || !strings.Contains(err.Error(), `unknown field "runner"`) {
		t.Fatalf("error=%v", err)
	}
}

func TestBootstrapPreflightNeedsNoLiveControlEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	binary := writePreflightFixture(t, `
test "$1" = node
test "$2" = config-check
test "$3" = --config
test -n "$4"
`)
	if err := preflightBootstrapCandidate(
		context.Background(), binary, filepath.Join(t.TempDir(), "config.json"),
	); err != nil {
		t.Fatal(err)
	}
}

func writePreflightFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bria")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
