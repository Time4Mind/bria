package nodelink_test

import (
	"errors"
	"testing"
	"time"

	"bria/internal/nodelink"
)

func TestPairingChallengeIsBoundSingleUseAndRevocable(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	pairing, err := nodelink.NewPairingRegistry()
	if err != nil {
		t.Fatal(err)
	}
	challenge := nodelink.PairingChallenge{
		ID:          "challenge-1",
		ComputerID:  "computer-1",
		Name:        "Laptop",
		Code:        "123456",
		Fingerprint: "sha256:laptop",
		ExpiresAt:   now.Add(time.Minute),
	}
	if err := pairing.Issue(challenge, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.Confirm(challenge.ID, challenge.Code, "sha256:other", now); !errors.Is(err, nodelink.ErrWrongPairingIdentity) {
		t.Fatalf("wrong identity error = %v", err)
	}
	grant, err := pairing.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if !pairing.Authorized(grant.ComputerID, grant.Fingerprint) {
		t.Fatal("confirmed computer is not authorized")
	}
	if _, err := pairing.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now); !errors.Is(err, nodelink.ErrPairingReplay) {
		t.Fatalf("replay error = %v, want ErrPairingReplay", err)
	}
	if err := pairing.Revoke(grant.ComputerID); err != nil {
		t.Fatal(err)
	}
	if pairing.Authorized(grant.ComputerID, grant.Fingerprint) {
		t.Fatal("revoked computer remains authorized")
	}
}

func TestPairingChallengeExpiresWithoutCreatingMembership(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	pairing, err := nodelink.NewPairingRegistry()
	if err != nil {
		t.Fatal(err)
	}
	challenge := nodelink.PairingChallenge{ID: "challenge-1", ComputerID: "computer-1", Name: "Laptop", Code: "123456", Fingerprint: "fp", ExpiresAt: now.Add(time.Minute)}
	if err := pairing.Issue(challenge, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now.Add(time.Minute)); !errors.Is(err, nodelink.ErrPairingExpired) {
		t.Fatalf("expired error = %v, want ErrPairingExpired", err)
	}
	if pairing.Authorized(challenge.ComputerID, challenge.Fingerprint) {
		t.Fatal("expired challenge created membership")
	}
}

func TestPairingReplayAndRevocationSurviveSnapshotRestore(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	pairing, err := nodelink.NewPairingRegistry()
	if err != nil {
		t.Fatal(err)
	}
	challenge := nodelink.PairingChallenge{ID: "challenge-1", ComputerID: "computer-1", Name: "Laptop", Code: "123456", Fingerprint: "fp", ExpiresAt: now.Add(time.Minute)}
	if err := pairing.Issue(challenge, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if err := pairing.Revoke(challenge.ComputerID); err != nil {
		t.Fatal(err)
	}

	restored, err := nodelink.RestorePairingRegistry(pairing.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Authorized(challenge.ComputerID, challenge.Fingerprint) {
		t.Fatal("revocation was lost after restore")
	}
	if _, err := restored.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now); !errors.Is(err, nodelink.ErrPairingReplay) {
		t.Fatalf("restored replay error = %v, want ErrPairingReplay", err)
	}
}

func TestPairingIdentityCannotBeGrantedToTwoComputerIDs(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	pairing, err := nodelink.NewPairingRegistry()
	if err != nil {
		t.Fatal(err)
	}
	first := nodelink.PairingChallenge{ID: "challenge-1", ComputerID: "computer-1", Name: "First", Code: "111111", Fingerprint: "shared-fingerprint", ExpiresAt: now.Add(time.Minute)}
	second := nodelink.PairingChallenge{ID: "challenge-2", ComputerID: "computer-2", Name: "Second", Code: "222222", Fingerprint: "shared-fingerprint", ExpiresAt: now.Add(time.Minute)}
	if err := pairing.Issue(first, now); err != nil {
		t.Fatal(err)
	}
	if err := pairing.Issue(second, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.Confirm(first.ID, first.Code, first.Fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.Confirm(second.ID, second.Code, second.Fingerprint, now); !errors.Is(err, nodelink.ErrPairingIdentityInUse) {
		t.Fatalf("second identity error = %v, want ErrPairingIdentityInUse", err)
	}
	if pairing.Authorized(second.ComputerID, second.Fingerprint) {
		t.Fatal("one identity authorized a second computer")
	}
}
