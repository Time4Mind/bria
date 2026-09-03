package backupruntime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/backupflow"
	"bria/internal/backupruntime"
)

func TestDirectoryActivatorCommitsOnlyAfterReopenReceipt(t *testing.T) {
	root := t.TempDir()
	live, candidate := filepath.Join(root, "live"), filepath.Join(root, "candidate")
	mustRuntimeWrite(t, filepath.Join(live, "state"), "old")
	mustRuntimeWrite(t, filepath.Join(candidate, "state"), "new")
	reopener := &recordingReopener{receipt: "reopen-1"}
	activator, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{LiveDirectory: live, MarkerPath: filepath.Join(root, "restore.commit"), Reopener: reopener})
	if err != nil {
		t.Fatalf("NewDirectoryActivator: %v", err)
	}
	request := backupflow.ActivationRequest{CandidateDir: candidate, ComputerID: "mac", Fingerprint: "sha256-new"}
	receipt, err := activator.Activate(context.Background(), request)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	assertRuntimeContent(t, filepath.Join(live, "state"), "new")
	if receipt.ReceiptID != "reopen-1" || receipt.CandidateDir != candidate || receipt.ComputerID != "mac" || receipt.Fingerprint != "sha256-new" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if _, err := os.Stat(live + ".previous"); !os.IsNotExist(err) {
		t.Fatalf("previous remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "restore.commit")); !os.IsNotExist(err) {
		t.Fatalf("marker remains: %v", err)
	}
}

func TestBackupflowPreparedRestoreActivatesThroughDirectoryAdapter(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	for relative, content := range map[string]string{
		"settings/bria.json": "settings", "computers/catalog.json": "computers",
		"sessions/state.json": "sessions", "messages/undelivered.json": "messages",
		"history/text/session.jsonl": "history", "harness/rules/rules.md": "rules",
		"harness/settings/settings.json": "{}", "harness/checks/check.go": "package checks",
		"harness/state/state.json": "{}",
	} {
		mustRuntimeWrite(t, filepath.Join(source, filepath.FromSlash(relative)), content)
	}
	flow := backupflow.Service{
		SourceRoot: source, LatestPath: filepath.Join(root, "latest.bria-backup"),
		RestoreCandidateDir: filepath.Join(root, "restore-candidate"), ComputerID: "mac",
		Layout: backupflow.CanonicalSnapshotLayout(),
	}
	if _, err := flow.CreateLatest(); err != nil {
		t.Fatalf("CreateLatest: %v", err)
	}
	prepared, err := flow.PrepareRestore()
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	live := filepath.Join(root, "live")
	mustRuntimeWrite(t, filepath.Join(live, "old"), "old")
	activator, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{LiveDirectory: live, MarkerPath: filepath.Join(root, "restore.commit"), Reopener: &recordingReopener{receipt: "physical-reopen"}})
	if err != nil {
		t.Fatalf("NewDirectoryActivator: %v", err)
	}
	receipt, err := flow.ActivateRestore(context.Background(), prepared, activator)
	if err != nil {
		t.Fatalf("ActivateRestore: %v", err)
	}
	if receipt.ReceiptID != "physical-reopen" {
		t.Fatalf("receipt = %#v", receipt)
	}
	assertRuntimeContent(t, filepath.Join(live, "sessions", "state.json"), "sessions")
	if _, err := os.Stat(filepath.Join(live, "old")); !os.IsNotExist(err) {
		t.Fatalf("old state remains: %v", err)
	}
}

func TestDirectoryActivatorRollsBackAndPreservesCandidateWhenReopenFails(t *testing.T) {
	root := t.TempDir()
	live, candidate := filepath.Join(root, "live"), filepath.Join(root, "candidate")
	mustRuntimeWrite(t, filepath.Join(live, "state"), "old")
	mustRuntimeWrite(t, filepath.Join(candidate, "state"), "new")
	activator, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{LiveDirectory: live, MarkerPath: filepath.Join(root, "restore.commit"), Reopener: &recordingReopener{err: errors.New("cannot reopen")}})
	if err != nil {
		t.Fatalf("NewDirectoryActivator: %v", err)
	}
	if _, err := activator.Activate(context.Background(), backupflow.ActivationRequest{CandidateDir: candidate, ComputerID: "mac", Fingerprint: "sha256-new"}); err == nil {
		t.Fatal("Activate error = nil")
	}
	assertRuntimeContent(t, filepath.Join(live, "state"), "old")
	assertRuntimeContent(t, filepath.Join(candidate, "state"), "new")
}

func TestDirectoryActivatorRecoversInterruptedDurableCommit(t *testing.T) {
	root := t.TempDir()
	live, candidate, marker := filepath.Join(root, "live"), filepath.Join(root, "candidate"), filepath.Join(root, "restore.commit")
	mustRuntimeWrite(t, filepath.Join(live, "state"), "old")
	mustRuntimeWrite(t, filepath.Join(candidate, "state"), "new")
	crashed, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{
		LiveDirectory: live, MarkerPath: marker, Reopener: &recordingReopener{receipt: "unused"},
		AfterStage: func(stage string) error {
			if stage == "old_moved" {
				return errors.New("simulated process stop")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewDirectoryActivator(crashed): %v", err)
	}
	request := backupflow.ActivationRequest{CandidateDir: candidate, ComputerID: "mac", Fingerprint: "sha256-new"}
	if _, err := crashed.Activate(context.Background(), request); err == nil {
		t.Fatal("interrupted Activate error = nil")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("durable marker missing: %v", err)
	}
	recovered, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{LiveDirectory: live, MarkerPath: marker, Reopener: &recordingReopener{receipt: "recovered-1"}})
	if err != nil {
		t.Fatalf("NewDirectoryActivator(recovery): %v", err)
	}
	receipt, found, err := recovered.Recover(context.Background())
	if err != nil || !found {
		t.Fatalf("Recover = %#v, %v, %v", receipt, found, err)
	}
	if receipt.ReceiptID != "recovered-1" {
		t.Fatalf("receipt = %#v", receipt)
	}
	assertRuntimeContent(t, filepath.Join(live, "state"), "new")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker remains: %v", err)
	}
}

func TestDirectoryActivatorRecoversInterruptedFirstInstall(t *testing.T) {
	root := t.TempDir()
	live, candidate, marker := filepath.Join(root, "live"), filepath.Join(root, "candidate"), filepath.Join(root, "restore.commit")
	mustRuntimeWrite(t, filepath.Join(candidate, "state"), "new")
	crashed, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{
		LiveDirectory: live, MarkerPath: marker, Reopener: &recordingReopener{receipt: "unused"},
		AfterStage: func(stage string) error {
			if stage == "old_moved" {
				return errors.New("simulated process stop")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewDirectoryActivator(crashed): %v", err)
	}
	request := backupflow.ActivationRequest{CandidateDir: candidate, ComputerID: "mac", Fingerprint: "sha256-new"}
	if _, err := crashed.Activate(context.Background(), request); err == nil {
		t.Fatal("interrupted Activate error = nil")
	}
	recovered, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{LiveDirectory: live, MarkerPath: marker, Reopener: &recordingReopener{receipt: "first-install"}})
	if err != nil {
		t.Fatalf("NewDirectoryActivator(recovery): %v", err)
	}
	receipt, found, err := recovered.Recover(context.Background())
	if err != nil || !found || receipt.ReceiptID != "first-install" {
		t.Fatalf("Recover = %#v, %v, %v", receipt, found, err)
	}
	assertRuntimeContent(t, filepath.Join(live, "state"), "new")
}

type recordingReopener struct {
	receipt string
	err     error
}

func (r *recordingReopener) Reopen(_ context.Context, live, fingerprint string) (string, error) {
	if live == "" || fingerprint == "" {
		return "", errors.New("missing reopen identity")
	}
	return r.receipt, r.err
}

func mustRuntimeWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func assertRuntimeContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}
