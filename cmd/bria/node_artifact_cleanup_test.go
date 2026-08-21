package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupNodeArtifactsRemovesExpiredRestoreMarker(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(2_000_000, 0).UTC()
	marker := filepath.Join(root, "restore.applied.0123456789abcdef.json")
	if err := os.WriteFile(marker, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := cleanupNodeArtifacts(root, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker still exists: %v", err)
	}
}
