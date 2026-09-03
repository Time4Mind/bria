package updateinstall_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/update"
	"bria/internal/updateinstall"
)

func TestVersionedPostflightReturnsExactHealthAndRereadState(t *testing.T) {
	t.Parallel()
	health := update.HealthReceipt{
		NodeID: "executor", RunningVersion: "2.0.0", Started: true, StateReadable: true,
		ProvidersAvailable: true, CoordinatorConnected: true, SessionsAvailable: true, ProbeSucceeded: true,
	}
	probe := updateinstall.VersionedPostflight{
		Health:    fixedHealthReader{receipt: health},
		State:     &sequenceStateReader{states: []updateinstall.InstalledState{{Version: "2.0.0", StateFingerprint: "state-v1"}}},
		Integrity: fixedIntegrityVerifier{},
	}

	receipt, err := probe.Probe(context.Background(), "executor", "2.0.0")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if receipt.Health != health || receipt.StateFingerprint != "state-v1" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestVersionedPostflightRejectsStateForAnotherVersion(t *testing.T) {
	t.Parallel()
	probe := updateinstall.VersionedPostflight{
		Health:    fixedHealthReader{receipt: update.HealthReceipt{NodeID: "executor", RunningVersion: "2.0.0"}},
		State:     &sequenceStateReader{states: []updateinstall.InstalledState{{Version: "1.0.0", StateFingerprint: "state-v1"}}},
		Integrity: fixedIntegrityVerifier{},
	}

	if _, err := probe.Probe(context.Background(), "executor", "2.0.0"); !errors.Is(err, updateinstall.ErrPostflightMismatch) {
		t.Fatalf("Probe error = %v, want ErrPostflightMismatch", err)
	}
}

type fixedIntegrityVerifier struct{ err error }

func (v fixedIntegrityVerifier) VerifyCurrent(context.Context, string, string) error { return v.err }

type fixedHealthReader struct {
	receipt update.HealthReceipt
	err     error
}

func (r fixedHealthReader) ReadHealth(context.Context, string, string) (update.HealthReceipt, error) {
	return r.receipt, r.err
}
