// Package archiveimport validates and merges externally discovered archived
// sessions without persistence or provider-specific knowledge.
package archiveimport

import (
	"errors"
	"fmt"

	"bria/internal/domain"
)

var ErrConflict = errors.New("archived session import conflict")

// Merge returns a cloned next state. Any invalid candidate or conflict fails
// the complete batch and leaves current untouched.
func Merge(current map[domain.IntentID]domain.Session, candidates []domain.Session) (map[domain.IntentID]domain.Session, bool, error) {
	next := make(map[domain.IntentID]domain.Session, len(current)+len(candidates))
	byID := make(map[domain.SessionID]domain.IntentID, len(current)+len(candidates))
	byProvider := make(map[providerIdentity]domain.SessionID, len(current)+len(candidates))
	for intentID, session := range current {
		if err := validSession(session); err != nil {
			return nil, false, err
		}
		if existingIntent, duplicate := byID[session.ID()]; duplicate && existingIntent != intentID {
			return nil, false, ErrConflict
		}
		next[intentID] = session
		byID[session.ID()] = intentID
		if binding, ok := session.Binding(); ok {
			provider := providerIdentity{session.ComputerID(), binding.Provider, binding.SessionID}
			if existingID, duplicate := byProvider[provider]; duplicate && existingID != session.ID() {
				return nil, false, ErrConflict
			}
			byProvider[provider] = session.ID()
		}
	}
	changed := false
	for _, candidate := range candidates {
		if err := validSession(candidate); err != nil || candidate.Status() != domain.SessionArchived {
			return nil, false, fmt.Errorf("%w: candidate is not a valid archived session", ErrConflict)
		}
		if intentID, exists := byID[candidate.ID()]; exists {
			if !next[intentID].Equal(candidate) {
				return nil, false, ErrConflict
			}
			continue
		}
		if existing, exists := next[candidate.IntentID()]; exists {
			if !existing.Equal(candidate) {
				return nil, false, ErrConflict
			}
			continue
		}
		binding, _ := candidate.Binding()
		provider := providerIdentity{candidate.ComputerID(), binding.Provider, binding.SessionID}
		if existingID, exists := byProvider[provider]; exists && existingID != candidate.ID() {
			return nil, false, ErrConflict
		}
		next[candidate.IntentID()] = candidate
		byID[candidate.ID()] = candidate.IntentID()
		byProvider[provider] = candidate.ID()
		changed = true
	}
	return next, changed, nil
}

func Equal(left, right map[domain.IntentID]domain.Session) bool {
	if len(left) != len(right) {
		return false
	}
	for intentID, session := range left {
		other, exists := right[intentID]
		if !exists || !session.Equal(other) {
			return false
		}
	}
	return true
}

func validSession(session domain.Session) error {
	restored, err := domain.RestoreSession(session.Snapshot())
	if err != nil || !restored.Equal(session) {
		return ErrConflict
	}
	return nil
}

type providerIdentity struct {
	computer domain.ComputerID
	provider domain.Provider
	session  string
}
