package nodelink_test

import (
	"path/filepath"
	"testing"
	"time"

	"bria/internal/nodelink"
)

func TestPairingFilePreservesConsumedGrantAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	store, err := nodelink.OpenPairingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	registry, _ := nodelink.NewPairingRegistry()
	challenge := nodelink.PairingChallenge{ID: "challenge-1", ComputerID: "executor-1", Name: "Laptop", Code: "123456", Fingerprint: "sha256:executor", ExpiresAt: now.Add(time.Minute)}
	if err := registry.Issue(challenge, now); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(registry.Snapshot()); err != nil {
		t.Fatal(err)
	}
	reopened, err := nodelink.OpenPairingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := nodelink.RestorePairingRegistry(reopened.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Authorized("executor-1", "sha256:executor") {
		t.Fatal("pairing grant was not durable")
	}
}

func TestPairingFileCommitsConfirmationBeforeReturning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	store, err := nodelink.OpenPairingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC)
	challenge := nodelink.PairingChallenge{ID: "challenge-2", ComputerID: "executor-2", Name: "Server", Code: "654321", Fingerprint: "sha256:server", ExpiresAt: now.Add(time.Minute)}
	if err := store.Issue(challenge, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := nodelink.OpenPairingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := reopened.ComputerForFingerprint(challenge.Fingerprint); !ok || id != challenge.ComputerID {
		t.Fatalf("ComputerForFingerprint = %q, %v", id, ok)
	}
}

func TestPairingFileCompactsExpiredReplayTombstones(t *testing.T) {
	store, err := nodelink.OpenPairingFile(filepath.Join(t.TempDir(), "pairing.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 13, 0, 0, 0, time.UTC)
	challenge, _ := nodelink.NewPairingChallenge("challenge-1", "executor-1", "Laptop", "123456", "sha256:executor", now.Add(time.Minute))
	if err := store.Issue(challenge, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot().Consumed); got != 1 {
		t.Fatalf("consumed tombstones = %d, want 1", got)
	}
	if err := store.PruneExpired(challenge.ExpiresAt.Add(nodelink.PairingReplayRetention)); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot().Consumed); got != 0 {
		t.Fatalf("consumed tombstones after retention = %d, want 0", got)
	}
}
