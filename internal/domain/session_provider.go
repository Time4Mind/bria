package domain

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// BindProviderSession records the provider-owned identity discovered after a
// fresh launch. Codex does not support assigning this identity at startup.
func (s *State) BindProviderSession(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	providerID string,
	at time.Time,
) error {
	if !s.CanPerformSessionAction(actorID, ref, ActionSendInput) {
		return ErrAccessDenied
	}
	session, err := s.mutableLiveSession(ref)
	if err != nil {
		return err
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	providerID = strings.TrimSpace(providerID)
	if !providerIDPattern.MatchString(providerID) {
		return fmt.Errorf("%w: provider session id is invalid", ErrInvalidState)
	}
	if session.ProviderSessionID != "" && session.ProviderSessionID != providerID {
		return ErrInvalidState
	}
	if session.ProviderSessionID == providerID {
		return nil
	}
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.ProviderSessionID = providerID
	session.LastEventAt = at
	session.Revision++
	s.Sessions[ref.Key()] = session
	return nil
}
