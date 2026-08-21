package clusterupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestConfirmInstalledRequiresExactBinaryIdentity(t *testing.T) {
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.Mkdir(releases, 0o700); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(releases, "previous-release")
	next := filepath.Join(releases, "next-release")
	for _, item := range []struct {
		path, content string
	}{{previous, "previous"}, {next, "next"}} {
		if err := os.Mkdir(item.path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(item.path, "bria"), []byte(item.content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	previousDigest := sha256.Sum256([]byte("previous"))
	nextDigest := sha256.Sum256([]byte("next"))
	current := filepath.Join(root, "current")
	if err := os.Symlink(next, current); err != nil {
		t.Fatal(err)
	}
	pending := pendingUpdate{
		UpdateID: "job", Version: "v2", Previous: previous,
		PreviousSHA256: hex.EncodeToString(previousDigest[:]), Next: next,
		NextSHA256: hex.EncodeToString(nextDigest[:]), CurrentLink: current,
	}
	path := filepath.Join(root, "update-pending.json")
	if err := writePending(path, pending); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmInstalled(root, "v2", strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong running binary identity confirmed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("wrong identity disarmed rollback: %v", err)
	}
	if err := ConfirmInstalled(root, "v2", pending.NextSHA256); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmInstalledAbortsPreparedSwitchStillOnPrevious(t *testing.T) {
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.Mkdir(releases, 0o700); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(releases, "previous")
	if err := os.Mkdir(previous, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("previous")
	if err := os.WriteFile(filepath.Join(previous, "bria"), content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	current := filepath.Join(root, "current")
	if err := os.Symlink(previous, current); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "update-pending.json")
	if err := writePending(path, pendingUpdate{
		UpdateID: "job", Version: "v2", Previous: previous,
		PreviousSHA256: hex.EncodeToString(digest[:]), Next: filepath.Join(releases, "next"),
		NextSHA256: strings.Repeat("a", 64), CurrentLink: current,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmInstalled(root, "v1", hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("prepared rollback record remains: %v", err)
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

func TestExactReleaseProvenanceRejectsUnknownOrInjectedValues(t *testing.T) {
	for _, value := range []string{"", "unknown", "abc123", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("g", 40)} {
		if exactReleaseCommit(value) {
			t.Fatalf("commit %q was accepted", value)
		}
	}
	if !exactReleaseCommit("0123456789abcdef0123456789abcdef01234567") {
		t.Fatal("full lowercase commit was rejected")
	}
	for _, value := range []string{"", "unknown", "1 -X forged", strings.Repeat("1", 21)} {
		if exactReleaseTimestamp(value) {
			t.Fatalf("timestamp %q was accepted", value)
		}
	}
	if !exactReleaseTimestamp("1750000000") {
		t.Fatal("numeric build timestamp was rejected")
	}
}

func TestReleaseReuseAcceptsCanonicalMetadataFromAnotherInstaller(t *testing.T) {
	left, right := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
	content := []byte("same runtime binary")
	digest := sha256.Sum256(content)
	identity := hex.EncodeToString(digest[:])
	for _, root := range []string{left, right} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "bria"), content, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	localMetadata := []byte(`{"schema":1,"name":"bria","version":"v2","commit":"0123456789abcdef0123456789abcdef01234567","built_at":"1750000000","binary_sha256":"` + identity + `","node_protocol":1}`)
	clusterMetadata := []byte(`{"schema":1,"version":"v2","commit":"0123456789abcdef0123456789abcdef01234567","built_at":"1750000000","binary_sha256":"` + identity + `","artifact_sha256":"` + strings.Repeat("a", 64) + `","node_protocol":1}`)
	if err := os.WriteFile(filepath.Join(left, "release.json"), localMetadata, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "release.json"), clusterMetadata, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalledReleaseMetadata(left); err != nil {
		t.Fatal(err)
	}
	equal, err := sameRuntimeReleasePayload(left, right)
	if err != nil || !equal {
		t.Fatalf("canonical cross-installer payload equality = %t, %v", equal, err)
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
		if err := os.WriteFile(filepath.Join(path, "bria"), []byte("bounded rollback fixture"), 0o700); err != nil {
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
