package backup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/backup"
)

func TestPromoteRejectsCorruptCandidateAndPreservesLatest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	mustWrite(t, filepath.Join(source, "settings.json"), "old-settings")
	latest := filepath.Join(root, "latest.bria-backup")
	goodCandidate := filepath.Join(root, "good.candidate")
	plan := backup.Plan{
		ComputerID: "macbook",
		Includes:   []backup.Include{{Path: "settings.json", Class: backup.ClassSettings}},
	}
	if _, err := backup.BuildCandidate(source, goodCandidate, plan); err != nil {
		t.Fatalf("BuildCandidate(good): %v", err)
	}
	if _, err := backup.PromoteCandidate(goodCandidate, latest); err != nil {
		t.Fatalf("PromoteCandidate(good): %v", err)
	}
	before, err := os.ReadFile(latest)
	if err != nil {
		t.Fatalf("read latest before corrupt promotion: %v", err)
	}

	mustWrite(t, filepath.Join(source, "settings.json"), "new-settings")
	corruptCandidate := filepath.Join(root, "corrupt.candidate")
	if _, err := backup.BuildCandidate(source, corruptCandidate, plan); err != nil {
		t.Fatalf("BuildCandidate(to corrupt): %v", err)
	}
	corruptBytes, err := os.ReadFile(corruptCandidate)
	if err != nil {
		t.Fatalf("read candidate to corrupt: %v", err)
	}
	payloadOffset := bytes.Index(corruptBytes, []byte("new-settings"))
	if payloadOffset < 0 {
		t.Fatal("candidate payload not found")
	}
	corruptBytes[payloadOffset] ^= 0xff
	if err := os.WriteFile(corruptCandidate, corruptBytes, 0o600); err != nil {
		t.Fatalf("corrupt candidate: %v", err)
	}
	if _, err := backup.PromoteCandidate(corruptCandidate, latest); err == nil {
		t.Fatal("PromoteCandidate(corrupt) error = nil")
	}
	after, err := os.ReadFile(latest)
	if err != nil {
		t.Fatalf("read latest after corrupt promotion: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("corrupt candidate changed the last confirmed backup")
	}
	if _, err := os.Stat(corruptCandidate); !os.IsNotExist(err) {
		t.Fatalf("corrupt candidate still exists, stat error = %v", err)
	}
}

func TestBackupUsesAllowlistAndExplicitSecretExclusionThenRestoresAndRereads(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	mustWrite(t, filepath.Join(source, "settings", "public.json"), "public")
	mustWrite(t, filepath.Join(source, "settings", "telegram-token"), "TOP-SECRET")
	mustWrite(t, filepath.Join(source, "logs", "bria.log"), "must-not-leak")
	mustWrite(t, filepath.Join(source, "history", "session.jsonl"), "text history")

	candidate := filepath.Join(root, "candidate.bria-backup")
	plan := backup.Plan{
		ComputerID: "executor-1",
		Includes: []backup.Include{
			{Path: "settings", Class: backup.ClassSettings},
			{Path: "history", Class: backup.ClassHistory},
		},
		Excludes: []backup.Exclude{{Path: "settings/telegram-token", Reason: "telegram credential"}},
	}
	manifest, err := backup.BuildCandidate(source, candidate, plan)
	if err != nil {
		t.Fatalf("BuildCandidate(): %v", err)
	}
	if manifest.FormatVersion != backup.FormatVersion || manifest.ComputerID != "executor-1" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if got, want := manifest.Excludes, plan.Excludes; !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest excludes = %#v, want %#v", got, want)
	}
	latest := filepath.Join(root, "latest.bria-backup")
	if _, err := backup.PromoteCandidate(candidate, latest); err != nil {
		t.Fatalf("PromoteCandidate(): %v", err)
	}
	archiveBytes, err := os.ReadFile(latest)
	if err != nil {
		t.Fatalf("read promoted backup: %v", err)
	}
	for _, forbiddenContent := range [][]byte{[]byte("TOP-SECRET"), []byte("must-not-leak")} {
		if bytes.Contains(archiveBytes, forbiddenContent) {
			t.Fatalf("backup contains forbidden content %q", forbiddenContent)
		}
	}

	restored := filepath.Join(root, "restored-candidate")
	restoredManifest, err := backup.RestoreCandidate(latest, restored)
	if err != nil {
		t.Fatalf("RestoreCandidate(): %v", err)
	}
	if !reflect.DeepEqual(restoredManifest, manifest) {
		t.Fatalf("restored manifest differs\n got: %#v\nwant: %#v", restoredManifest, manifest)
	}
	assertContent(t, filepath.Join(restored, "settings", "public.json"), "public")
	assertContent(t, filepath.Join(restored, "history", "session.jsonl"), "text history")
	for _, forbidden := range []string{
		filepath.Join(restored, "settings", "telegram-token"),
		filepath.Join(restored, "logs", "bria.log"),
	} {
		if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("forbidden path %q restored, stat error = %v", forbidden, err)
		}
	}
}

func TestBuildCandidateRejectsUnknownClassAndSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "plain"), "value")
	if _, err := backup.BuildCandidate(root, filepath.Join(t.TempDir(), "candidate"), backup.Plan{
		ComputerID: "host",
		Includes:   []backup.Include{{Path: "plain", Class: backup.Class("credentials")}},
	}); err == nil {
		t.Fatal("unknown backup class accepted")
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "plain"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := backup.BuildCandidate(root, filepath.Join(t.TempDir(), "candidate"), backup.Plan{
		ComputerID: "host",
		Includes:   []backup.Include{{Path: "link", Class: backup.ClassSettings}},
	}); err == nil {
		t.Fatal("symlink include accepted")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("content %q = %q, want %q", path, got, want)
	}
}
