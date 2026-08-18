package clusterupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractReleaseAllowsIsolatedRunnerToTraverseRelease(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	data := []byte("binary")
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "bria", Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "release")
	if err := extractRelease(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("release directory mode = %o, want 755", got)
	}
}

func TestExtractReleaseRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	_ = tarWriter.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 1})
	_, _ = tarWriter.Write([]byte("x"))
	_ = tarWriter.Close()
	_ = gzipWriter.Close()
	_ = file.Close()
	if err := extractRelease(archivePath, filepath.Join(t.TempDir(), "release")); err == nil {
		t.Fatal("path traversal archive was accepted")
	}
}

func TestWatchdogRestoresPreviousTarget(t *testing.T) {
	root := t.TempDir()
	previous := filepath.Join(root, "previous")
	current := filepath.Join(root, "current")
	newTarget := filepath.Join(root, "new")
	for _, path := range []string{previous, newTarget} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(newTarget, current); err != nil {
		t.Fatal(err)
	}
	pending := pendingUpdate{UpdateID: "job", Version: "v2", Previous: previous, CurrentLink: current}
	data, _ := json.Marshal(pending)
	if err := os.WriteFile(filepath.Join(root, "update-pending.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Watchdog(context.Background(), root, "job", 999999, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(current)
	wantTarget, wantErr := filepath.EvalSymlinks(previous)
	if err != nil || wantErr != nil || target != wantTarget {
		t.Fatalf("rollback target = %q, err=%v", target, err)
	}
	statusData, err := os.ReadFile(filepath.Join(root, "update-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(statusData, &status); err != nil || status.Phase != PhaseFailed ||
		status.NodeID != "" || status.UpdateID != "job" || status.Version != "v2" {
		t.Fatalf("rollback status = %#v, err=%v", status, err)
	}
}
