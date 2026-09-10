package recoverybarrier_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/recoverybackoff"
	"bria/internal/supervisioncomposition/recoverybarrier"
)

type barrierError string

func (err barrierError) Error() string                         { return "stable barrier" }
func (err barrierError) StableRecoveryBarrierRevision() string { return string(err) }

type blockingRevision struct {
	started chan struct{}
	release chan struct{}
}

func (source blockingRevision) RecoveryEvidenceRevision(context.Context, domain.SessionID, domain.ProviderBinding) (string, error) {
	close(source.started)
	<-source.release
	return "v2", nil
}

func TestStaleEvidenceProbeCannotReleaseNewerBarrier(t *testing.T) {
	source := blockingRevision{started: make(chan struct{}), release: make(chan struct{})}
	gate := recoverybarrier.New(source)
	identity := recoverybackoff.Identity{Lifecycle: time.Unix(1, 0)}
	gate.Failed("s", identity, barrierError("v1"), time.Now())
	result := make(chan bool, 1)
	go func() { result <- gate.Blocked(context.Background(), "s", identity) }()
	<-source.started
	gate.Failed("s", identity, barrierError("v3"), time.Now())
	close(source.release)
	if !<-result || gate.Ready("s", identity, time.Now().Add(time.Hour)) {
		t.Fatal("stale evidence probe released a newer stable barrier")
	}
}
