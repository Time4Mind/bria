package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/processlog"
)

func TestBinarySHA256ReadsExactRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bria")
	content := []byte("immutable Bria executable")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(content)
	got, err := binarySHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("binary SHA-256 = %q", got)
	}
}

func TestBinarySHA256RejectsNonRegularAndOversizedInputs(t *testing.T) {
	if _, err := binarySHA256(t.TempDir()); err == nil {
		t.Fatal("directory accepted as executable identity input")
	}
	path := filepath.Join(t.TempDir(), "oversized")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxBriaExecutableBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := binarySHA256(path); err == nil {
		t.Fatal("oversized executable accepted")
	}
}

func TestBuildVersionReportsCurrentBinaryIdentity(t *testing.T) {
	output, err := buildVersion()
	if err != nil {
		t.Fatal(err)
	}
	if len(output.BinarySHA256) != 64 || output.BuiltAt == "" {
		t.Fatalf("build identity = %#v", output)
	}
}

func TestBuildIdentityLabelsRejectLogfmtAndPathInjection(t *testing.T) {
	for _, value := range []string{"/tmp/secret", "x y", `x="secret"`, "line\nbreak"} {
		if validBuildVersion(value) {
			t.Fatalf("unsafe build version accepted: %q", value)
		}
	}
	if !validBuildVersion("v0.1.29-12-gabcdef0-dirty") ||
		!validBuildCommit("31a7f5ea4c9519b67ceed2d171c78a5a6fbf027a") ||
		validBuildCommit("not-a-commit") {
		t.Fatal("build label validation mismatch")
	}
}

func TestReleaseIdentityRejectsActivationSymlinkRace(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "releases", "new")
	if err := os.MkdirAll(release, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(release, "bria")
	if err := os.WriteFile(executable, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"version":"v2","commit":"bbbbbbb","built_at":"2","binary_sha256":"` +
		strings.Repeat("b", 64) + `"}`)
	if err := os.WriteFile(filepath.Join(release, "release.json"), manifest, 0o400); err != nil {
		t.Fatal(err)
	}
	err := verifyReleaseIdentity(executable, versionOutput{
		Version: "v1", Commit: "aaaaaaa", BuiltAt: "1", BinarySHA256: strings.Repeat("b", 64),
	})
	if err == nil {
		t.Fatal("new symlink target was accepted as the old running build")
	}
}

func TestRunningIdentityRejectsLaunchExpectedHashMismatch(t *testing.T) {
	t.Setenv(expectedBinarySHA256Env, strings.Repeat("0", 64))
	build, err := buildVersion()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRunningReleaseIdentity(build); err == nil {
		t.Fatal("launch identity mismatch was accepted")
	}
}

func TestStartupLogContainsContentFreeBuildIdentity(t *testing.T) {
	root := t.TempDir()
	manager, err := processlog.Start(root, processlog.Identity{Version: "v1", Commit: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	logNodeBuildIdentity(versionOutput{
		Version: "v1", Commit: "abc123", BuiltAt: "123",
		BinarySHA256: strings.Repeat("a", 64),
	})
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "service-") {
			body, err = os.ReadFile(filepath.Join(root, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	line := string(body)
	for _, want := range []string{
		`build_version="v1"`, `build_commit="abc123"`, `built_at="123"`,
		"binary_sha256=" + strings.Repeat("a", 64), "outcome=identified",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("startup log %q does not contain %q", line, want)
		}
	}
	if strings.Contains(line, root) {
		t.Fatalf("startup log leaked path: %q", line)
	}
}
