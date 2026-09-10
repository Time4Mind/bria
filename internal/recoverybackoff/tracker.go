package recoverybackoff

import (
	"errors"
	"time"

	"bria/internal/domain"
)

var ErrInvalidPolicy = errors.New("recovery backoff policy is invalid")

type Identity struct {
	Binding   domain.ProviderBinding
	Lifecycle time.Time
	Initial   bool
}
type failure struct {
	identity       Identity
	retryAfter     time.Time
	nextDelay      time.Duration
	stableRevision string
}
type Tracker struct {
	base     time.Duration
	maximum  time.Duration
	failures map[domain.SessionID]failure
}

func New(base, maximum time.Duration) (*Tracker, error) {
	if base <= 0 || maximum <= 0 || base > maximum {
		return nil, ErrInvalidPolicy
	}
	return &Tracker{base: base, maximum: maximum, failures: make(map[domain.SessionID]failure)}, nil
}
func Default() *Tracker { tracker, _ := New(time.Minute, 20*time.Minute); return tracker }

func (tracker *Tracker) Ready(id domain.SessionID, identity Identity, now time.Time) bool {
	failed, exists := tracker.failures[id]
	if !exists || failed.identity != identity {
		delete(tracker.failures, id)
		return true
	}
	return failed.stableRevision == "" && !now.Before(failed.retryAfter)
}

func (tracker *Tracker) Failed(id domain.SessionID, identity Identity, now time.Time) {
	failed := tracker.failures[id]
	if failed.identity != identity {
		failed = failure{identity: identity}
	}
	failed.stableRevision = ""
	delay := failed.nextDelay
	if delay == 0 {
		delay = tracker.base
	}
	failed.retryAfter = now.Add(delay)
	failed.nextDelay = doubled(delay, tracker.maximum)
	tracker.failures[id] = failed
}

func (tracker *Tracker) BlockUntilEvidenceChanges(id domain.SessionID, identity Identity, revision string) {
	if revision != "" {
		tracker.failures[id] = failure{identity: identity, stableRevision: revision}
	}
}

func (tracker *Tracker) StableBarrier(id domain.SessionID, identity Identity) (string, bool) {
	failed, exists := tracker.failures[id]
	if !exists || failed.identity != identity || failed.stableRevision == "" {
		return "", false
	}
	return failed.stableRevision, true
}

func (tracker *Tracker) ReleaseIfEvidenceChanged(id domain.SessionID, identity Identity, revision string) bool {
	stable, blocked := tracker.StableBarrier(id, identity)
	if !blocked || revision == "" || revision == stable {
		return false
	}
	delete(tracker.failures, id)
	return true
}

func (tracker *Tracker) Retain(desired map[domain.SessionID]Identity) {
	for id, failed := range tracker.failures {
		if identity, exists := desired[id]; !exists || identity != failed.identity {
			delete(tracker.failures, id)
		}
	}
}

func (tracker *Tracker) Succeeded(id domain.SessionID) { delete(tracker.failures, id) }

func doubled(current, maximum time.Duration) time.Duration {
	if current > maximum/2 {
		return maximum
	}
	return current * 2
}
