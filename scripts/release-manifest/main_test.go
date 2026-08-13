package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectArtifactsAddsAndroidLinuxARM64Alias(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "bria_v1.2.3_linux_arm64.tar.gz")
	if err := os.WriteFile(path, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts, err := collectArtifacts(directory, "v1.2.3", "https://example.test/releases")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	linux, android := artifacts[0], artifacts[1]
	if linux.OS != "linux" || android.OS != "android" || linux.Arch != android.Arch ||
		linux.URL != android.URL || linux.SHA256 != android.SHA256 || linux.Size != android.Size {
		t.Fatalf("invalid Android alias: %#v", artifacts)
	}
}
