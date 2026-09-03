// Package nodelink defines Bria's versioned, transport-neutral computer link
// protocol. Network listeners and certificate implementations live outside it.
package nodelink

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
)

var (
	ErrInvalidPairing       = errors.New("invalid pairing challenge")
	ErrPairingNotFound      = errors.New("pairing challenge not found")
	ErrPairingReplay        = errors.New("pairing challenge was already used")
	ErrPairingExpired       = errors.New("pairing challenge expired")
	ErrWrongPairingCode     = errors.New("wrong pairing code")
	ErrWrongPairingIdentity = errors.New("pairing challenge belongs to another identity")
	ErrPairingIdentityInUse = errors.New("computer identity is already paired")
	ErrComputerNotPaired    = errors.New("computer is not paired")
)

type PairingChallenge struct {
	ID          string
	ComputerID  domain.ComputerID
	Name        string
	Code        string
	Fingerprint string
	ExpiresAt   time.Time
}

func NewPairingChallenge(id, computerID, name, code, fingerprint string, expiresAt time.Time) (PairingChallenge, error) {
	challenge := PairingChallenge{ID: id, ComputerID: domain.ComputerID(computerID), Name: name, Code: code, Fingerprint: fingerprint, ExpiresAt: expiresAt}
	if err := validateChallenge(challenge); err != nil {
		return PairingChallenge{}, err
	}
	return challenge, nil
}

type PairingGrant struct {
	ComputerID  domain.ComputerID
	Name        string
	Fingerprint string
	Revoked     bool
}

const PairingReplayRetention = 24 * time.Hour

type ConsumedChallenge struct {
	ID          string
	RetainUntil time.Time
}

// PairingSnapshot is a local coordinator persistence DTO. It includes pending
// one-time codes and therefore must never be logged or included in backup.
type PairingSnapshot struct {
	Pending  []PairingChallenge
	Consumed []ConsumedChallenge
	Grants   []PairingGrant
}

type PairingRegistry struct {
	mu       sync.RWMutex
	pending  map[string]PairingChallenge
	consumed map[string]ConsumedChallenge
	grants   map[domain.ComputerID]PairingGrant
}

func NewPairingRegistry() (*PairingRegistry, error) {
	return &PairingRegistry{
		pending:  make(map[string]PairingChallenge),
		consumed: make(map[string]ConsumedChallenge),
		grants:   make(map[domain.ComputerID]PairingGrant),
	}, nil
}

func RestorePairingRegistry(snapshot PairingSnapshot) (*PairingRegistry, error) {
	registry, _ := NewPairingRegistry()
	for _, consumed := range snapshot.Consumed {
		if strings.TrimSpace(consumed.ID) == "" || consumed.RetainUntil.IsZero() {
			return nil, fmt.Errorf("%w: consumed challenge id is empty", ErrInvalidPairing)
		}
		registry.consumed[consumed.ID] = consumed
	}
	for _, challenge := range snapshot.Pending {
		if err := validateChallenge(challenge); err != nil {
			return nil, err
		}
		if _, duplicate := registry.pending[challenge.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate challenge id", ErrInvalidPairing)
		}
		if _, consumed := registry.consumed[challenge.ID]; consumed {
			return nil, fmt.Errorf("%w: pending challenge is already consumed", ErrInvalidPairing)
		}
		registry.pending[challenge.ID] = challenge
	}
	for _, grant := range snapshot.Grants {
		if err := validateGrant(grant); err != nil {
			return nil, err
		}
		if _, duplicate := registry.grants[grant.ComputerID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate computer grant", ErrInvalidPairing)
		}
		for _, existing := range registry.grants {
			if constantTimeEqual(existing.Fingerprint, grant.Fingerprint) {
				return nil, ErrPairingIdentityInUse
			}
		}
		registry.grants[grant.ComputerID] = grant
	}
	return registry, nil
}

func (registry *PairingRegistry) Issue(challenge PairingChallenge, now time.Time) error {
	if registry == nil {
		return ErrInvalidPairing
	}
	if err := validateChallenge(challenge); err != nil {
		return err
	}
	if !now.Before(challenge.ExpiresAt) {
		return ErrPairingExpired
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.pending[challenge.ID]; exists {
		return fmt.Errorf("%w: duplicate challenge id", ErrInvalidPairing)
	}
	if _, consumed := registry.consumed[challenge.ID]; consumed {
		return ErrPairingReplay
	}
	registry.pending[challenge.ID] = challenge
	return nil
}

func (registry *PairingRegistry) Confirm(id, code, fingerprint string, now time.Time) (PairingGrant, error) {
	if registry == nil {
		return PairingGrant{}, ErrPairingNotFound
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, consumed := registry.consumed[id]; consumed {
		return PairingGrant{}, ErrPairingReplay
	}
	challenge, exists := registry.pending[id]
	if !exists {
		return PairingGrant{}, ErrPairingNotFound
	}
	if !now.Before(challenge.ExpiresAt) {
		return PairingGrant{}, ErrPairingExpired
	}
	if !constantTimeEqual(code, challenge.Code) {
		return PairingGrant{}, ErrWrongPairingCode
	}
	if !constantTimeEqual(fingerprint, challenge.Fingerprint) {
		return PairingGrant{}, ErrWrongPairingIdentity
	}
	for computerID, existing := range registry.grants {
		if computerID != challenge.ComputerID && constantTimeEqual(existing.Fingerprint, challenge.Fingerprint) {
			return PairingGrant{}, ErrPairingIdentityInUse
		}
	}
	grant := PairingGrant{ComputerID: challenge.ComputerID, Name: challenge.Name, Fingerprint: challenge.Fingerprint}
	delete(registry.pending, id)
	registry.consumed[id] = ConsumedChallenge{ID: id, RetainUntil: challenge.ExpiresAt.Add(PairingReplayRetention)}
	registry.grants[grant.ComputerID] = grant
	return grant, nil
}

func (registry *PairingRegistry) Revoke(id domain.ComputerID) error {
	if registry == nil {
		return ErrComputerNotPaired
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	grant, exists := registry.grants[id]
	if !exists {
		return ErrComputerNotPaired
	}
	grant.Revoked = true
	registry.grants[id] = grant
	return nil
}

func (registry *PairingRegistry) Authorized(id domain.ComputerID, fingerprint string) bool {
	if registry == nil {
		return false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	grant, exists := registry.grants[id]
	return exists && !grant.Revoked && constantTimeEqual(grant.Fingerprint, fingerprint)
}

func (registry *PairingRegistry) ComputerForFingerprint(fingerprint string) (domain.ComputerID, bool) {
	if registry == nil || strings.TrimSpace(fingerprint) == "" {
		return "", false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for id, grant := range registry.grants {
		if !grant.Revoked && constantTimeEqual(grant.Fingerprint, fingerprint) {
			return id, true
		}
	}
	return "", false
}

func (registry *PairingRegistry) Snapshot() PairingSnapshot {
	if registry == nil {
		return PairingSnapshot{}
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	snapshot := PairingSnapshot{}
	for _, challenge := range registry.pending {
		snapshot.Pending = append(snapshot.Pending, challenge)
	}
	for _, consumed := range registry.consumed {
		snapshot.Consumed = append(snapshot.Consumed, consumed)
	}
	for _, grant := range registry.grants {
		snapshot.Grants = append(snapshot.Grants, grant)
	}
	sort.Slice(snapshot.Pending, func(i, j int) bool { return snapshot.Pending[i].ID < snapshot.Pending[j].ID })
	sort.Slice(snapshot.Consumed, func(i, j int) bool { return snapshot.Consumed[i].ID < snapshot.Consumed[j].ID })
	sort.Slice(snapshot.Grants, func(i, j int) bool { return snapshot.Grants[i].ComputerID < snapshot.Grants[j].ComputerID })
	return snapshot
}

func validateChallenge(challenge PairingChallenge) error {
	if strings.TrimSpace(challenge.ID) == "" || strings.TrimSpace(string(challenge.ComputerID)) == "" || strings.TrimSpace(challenge.Name) == "" || strings.TrimSpace(challenge.Code) == "" || strings.TrimSpace(challenge.Fingerprint) == "" || challenge.ExpiresAt.IsZero() {
		return ErrInvalidPairing
	}
	return nil
}

func validateGrant(grant PairingGrant) error {
	if strings.TrimSpace(string(grant.ComputerID)) == "" || strings.TrimSpace(grant.Name) == "" || strings.TrimSpace(grant.Fingerprint) == "" {
		return ErrInvalidPairing
	}
	return nil
}

func constantTimeEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
