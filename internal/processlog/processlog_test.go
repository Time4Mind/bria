package processlog

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagerRoutesLevelsAndAdoptsSupervisorLog(t *testing.T) {
	root := t.TempDir()
	raw := filepath.Join(root, "node.log")
	if err := os.WriteFile(raw, []byte("startup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := Start(root, Identity{Version: "test-version", Commit: "test-commit"})
	if err != nil {
		t.Fatal(err)
	}
	Detailf("detail event")
	Servicef("service event")
	Criticalf("critical event")
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(raw); !os.IsNotExist(err) {
		t.Fatalf("raw node.log still present: %v", err)
	}
	assertLogContains(t, root, "detail-", "detail event")
	assertLogContains(t, root, "service-", "service event")
	assertLogContains(t, root, "detail-legacy-", "startup")
	assertLogContains(t, root, "critical-20", "critical event")
	assertLogSize(t, root, "critical-raw-", 0)
	if err := os.WriteFile(raw, []byte("panic fallback\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adoptRawLog(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertLogContains(t, root, "critical-raw-", "panic fallback")
}

func TestStructuredEnvelopeIsBoundedSingleLineAndCarriesIdentity(t *testing.T) {
	identity := Identity{Version: "v1.2.3-test", Commit: "abc123"}
	at := time.Date(2026, 8, 21, 12, 34, 56, 789, time.UTC)
	record := string(formatStructuredRecord(
		at, 42, identity, Service, FailureTimeout,
		"failed\npath=/Users/artem/private/file\tbad\x00"+strings.Repeat("x", maxStructuredBodyBytes),
	))
	for _, expected := range []string{
		"at=2026-08-21T12:34:56.000000789Z", "pid=42",
		`version="v1.2.3-test"`, `commit="abc123"`, "severity=service",
		"failure_class=timeout", "truncated=true", `failed\npath=[path]`, `\tbad\x00`,
	} {
		if !strings.Contains(record, expected) {
			t.Fatalf("record missing %q: %q", expected, record)
		}
	}
	if strings.Count(record, "\n") != 1 || strings.Contains(record, "/Users/") {
		t.Fatalf("record is not a sanitized physical line: %q", record)
	}
	if len(record) > maxStructuredBodyBytes+512 {
		t.Fatalf("record length=%d", len(record))
	}
}

func TestStartRejectsUnsafeIdentityAndStructuredWriterLeavesRawStreamsRaw(t *testing.T) {
	root := t.TempDir()
	if _, err := Start(root, Identity{Version: "bad\nversion", Commit: "abc"}); err == nil {
		t.Fatal("unsafe build identity was accepted")
	}
	manager, err := Start(root, Identity{Version: "test", Commit: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	Servicef("structured")
	raw := "raft raw " + strconv.Itoa(os.Getpid()) + "\n"
	if _, err := Writer(Service).Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	data := readLogTier(t, root, "service-")
	if !strings.Contains(data, "failure_class=none") || !strings.Contains(data, "structured") {
		t.Fatalf("structured row=%q", data)
	}
	if !strings.Contains(data, "\n"+raw) {
		t.Fatalf("raw writer was unexpectedly enveloped: %q", data)
	}
}

func readLogTier(t *testing.T, root, prefix string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		result.Write(data)
	}
	return result.String()
}

func assertLogSize(t *testing.T, root, prefix string, expected int64) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			info, infoErr := entry.Info()
			if infoErr != nil {
				t.Fatal(infoErr)
			}
			if info.Size() != expected {
				t.Fatalf("%s size=%d, want %d", entry.Name(), info.Size(), expected)
			}
			return
		}
	}
	t.Fatalf("no log has prefix %q", prefix)
}

func TestCleanupExpiredUsesIndependentTierTTL(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1_000_000, 0)
	files := map[string]time.Duration{
		"detail-old.log":     7 * time.Hour,
		"detail-fresh.log":   5 * time.Hour,
		"service-old.log":    25 * time.Hour,
		"service-fresh.log":  23 * time.Hour,
		"critical-old.log":   73 * time.Hour,
		"critical-fresh.log": 71 * time.Hour,
		"unmanaged.log":      30 * 24 * time.Hour,
	}
	for name, age := range files {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupExpired(root, now, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"detail-old.log", "service-old.log", "critical-old.log"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("expired %s retained: %v", name, err)
		}
	}
	for _, name := range []string{
		"detail-fresh.log", "service-fresh.log", "critical-fresh.log", "unmanaged.log",
	} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("fresh/unmanaged %s removed: %v", name, err)
		}
	}
	active := filepath.Join(root, "critical-active.log")
	if err := os.WriteFile(active, []byte("old active fallback"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-73 * time.Hour)
	if err := os.Chtimes(active, old, old); err != nil {
		t.Fatal(err)
	}
	if err := cleanupExpired(root, now, map[string]bool{active: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(active)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("active fallback was not truncated: size=%d", info.Size())
	}
}

func TestBucketWriterRotatesWithoutRewritingPriorBucket(t *testing.T) {
	root := t.TempDir()
	current := time.Unix(10_800, 0).UTC()
	writer := &bucketWriter{
		root: root, policy: policy{level: Detail, retention: 6 * time.Hour, bucket: 30 * time.Minute},
		fallback: os.Stderr, now: func() time.Time { return current },
	}
	if _, err := writer.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	firstPath := writer.openPath()
	current = current.Add(31 * time.Minute)
	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	secondPath := writer.openPath()
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath {
		t.Fatal("bucket did not rotate")
	}
	if data, err := os.ReadFile(firstPath); err != nil || string(data) != "first\n" {
		t.Fatalf("first bucket=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(secondPath); err != nil || string(data) != "second\n" {
		t.Fatalf("second bucket=%q err=%v", data, err)
	}
}

func assertLogContains(t *testing.T, root, prefix, expected string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr == nil && strings.Contains(string(data), expected) {
			return
		}
	}
	t.Fatalf("no %s log contains %q", prefix, expected)
}
