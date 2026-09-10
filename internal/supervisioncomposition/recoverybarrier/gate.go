// Package recoverybarrier coordinates stable evidence barriers with retry backoff.
package recoverybarrier

import (
	"context"
	"errors"
	"sync"
	"time"

	"bria/internal/domain"
	"bria/internal/recoverybackoff"
)

type barrierError interface{ StableRecoveryBarrierRevision() string }
type revisioner interface {
	RecoveryEvidenceRevision(context.Context, domain.SessionID, domain.ProviderBinding) (string, error)
}

type Gate struct {
	mu      sync.Mutex
	tracker *recoverybackoff.Tracker
	source  any
}

func New(source any) *Gate { return &Gate{tracker: recoverybackoff.Default(), source: source} }

func (gate *Gate) Ready(id domain.SessionID, identity recoverybackoff.Identity, now time.Time) bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.tracker.Ready(id, identity, now)
}

func (gate *Gate) Retain(desired map[domain.SessionID]recoverybackoff.Identity) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.tracker.Retain(desired)
}

func (gate *Gate) Succeeded(id domain.SessionID) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.tracker.Succeeded(id)
}

func (gate *Gate) Failed(id domain.SessionID, identity recoverybackoff.Identity, err error, now time.Time) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	var barrier barrierError
	if errors.As(err, &barrier) && barrier.StableRecoveryBarrierRevision() != "" {
		gate.tracker.BlockUntilEvidenceChanges(id, identity, barrier.StableRecoveryBarrierRevision())
		return
	}
	gate.tracker.Failed(id, identity, now)
}

func (gate *Gate) Blocked(ctx context.Context, id domain.SessionID, identity recoverybackoff.Identity) bool {
	gate.mu.Lock()
	stable, blocked := gate.tracker.StableBarrier(id, identity)
	gate.mu.Unlock()
	if !blocked {
		return false
	}
	source, ok := gate.source.(revisioner)
	if !ok {
		return true
	}
	revision, err := source.RecoveryEvidenceRevision(ctx, id, identity.Binding)
	if err != nil || revision == stable {
		return true
	}
	gate.mu.Lock()
	current, stillBlocked := gate.tracker.StableBarrier(id, identity)
	released := stillBlocked && current == stable && gate.tracker.ReleaseIfEvidenceChanged(id, identity, revision)
	gate.mu.Unlock()
	return !released
}
