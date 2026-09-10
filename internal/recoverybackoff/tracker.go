// Package recoverybackoff tracks retry eligibility for independently owned
// recovery identities. Callers provide synchronization.
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
	identity   Identity
	retryAfter time.Time
	nextDelay  time.Duration
}

// Tracker keeps exponential retry state without starting goroutines or taking
// locks. Identity must change whenever a binding or lifecycle generation does.
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

func Default() *Tracker {
	tracker, _ := New(time.Minute, 20*time.Minute)
	return tracker
}

// Ready reports whether an immediate attempt is allowed. A changed identity
// starts a fresh lifecycle and discards the prior delay.
func (tracker *Tracker) Ready(id domain.SessionID, identity Identity, now time.Time) bool {
	failed, exists := tracker.failures[id]
	if !exists {
		return true
	}
	if failed.identity != identity {
		delete(tracker.failures, id)
		return true
	}
	return !now.Before(failed.retryAfter)
}

// Failed records one real failed attempt and advances its uncapped retry
// sequence until the configured maximum is reached.
func (tracker *Tracker) Failed(id domain.SessionID, identity Identity, now time.Time) {
	failed := tracker.failures[id]
	if failed.identity != identity {
		failed = failure{identity: identity}
	}
	delay := failed.nextDelay
	if delay == 0 {
		delay = tracker.base
	}
	failed.retryAfter = now.Add(delay)
	failed.nextDelay = doubled(delay, tracker.maximum)
	tracker.failures[id] = failed
}

// Retain removes sessions which disappeared or changed lifecycle identity.
func (tracker *Tracker) Retain(desired map[domain.SessionID]Identity) {
	for id, failed := range tracker.failures {
		if identity, exists := desired[id]; !exists || identity != failed.identity {
			delete(tracker.failures, id)
		}
	}
}

func (tracker *Tracker) Succeeded(id domain.SessionID) {
	delete(tracker.failures, id)
}

func doubled(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}
