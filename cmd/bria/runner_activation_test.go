package main

import (
	"path/filepath"
	"testing"
)

func TestRunnerActivationPaths(t *testing.T) {
	activation, current := runnerActivationPaths(
		filepath.FromSlash("/opt/bria/releases/v0.1.12-job-linux-amd64/bria"),
	)
	if activation != filepath.FromSlash("/opt/bria/current") {
		t.Fatalf("activation = %q", activation)
	}
	if current != filepath.FromSlash("/opt/bria/releases/v0.1.12-job-linux-amd64") {
		t.Fatalf("current = %q", current)
	}
	if activation, _ := runnerActivationPaths(filepath.FromSlash("/usr/local/bin/bria")); activation != "" {
		t.Fatalf("unmanaged activation = %q", activation)
	}
}
