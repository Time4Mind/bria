package domain

import (
	"fmt"
	"strings"
	"time"
)

func normalizeNewSession(session *Session) error {
	providerID := strings.TrimSpace(session.ProviderSessionID)
	if providerID != "" && !providerIDPattern.MatchString(providerID) {
		return fmt.Errorf("%w: provider session id is invalid", ErrInvalidState)
	}
	if session.ProviderResume && providerID == "" {
		return fmt.Errorf("%w: resumed session requires a provider id", ErrInvalidState)
	}
	session.ProviderSessionID = providerID
	switch session.State {
	case "", SessionLive:
		session.State = SessionLive
	case SessionIdle:
		session.State = SessionLive
		session.RuntimePhase = RuntimeIdle
	case SessionRecovering:
		session.State = SessionLive
		session.RuntimePhase = RuntimeDegraded
		session.ResumePending = true
	default:
		return fmt.Errorf("%w: a new session must be live", ErrInvalidState)
	}
	if session.RuntimePhase == "" {
		session.RuntimePhase = RuntimeIdle
	}
	if !validRuntimePhase(session.RuntimePhase) {
		return fmt.Errorf("%w: unsupported runtime phase %q", ErrInvalidState, session.RuntimePhase)
	}
	if session.RuntimeGeneration == 0 {
		session.RuntimeGeneration = 1
	}
	if session.Revision == 0 {
		session.Revision = 1
	}
	if session.InteractivePrompt != nil {
		report := InteractivePromptReport{
			SessionID: session.ID, Generation: session.RuntimeGeneration, Present: true,
			Kind: session.InteractivePrompt.Kind, Hash: session.InteractivePrompt.Hash,
		}
		if session.RuntimePhase != RuntimeWaitingInput || report.validate() != nil {
			return fmt.Errorf("%w: interactive prompt is invalid", ErrInvalidState)
		}
	} else if session.RuntimePhase == RuntimeWaitingInput {
		return fmt.Errorf("%w: waiting session requires interactive prompt", ErrInvalidState)
	}
	if session.ProviderBindingSince.IsZero() {
		session.ProviderBindingSince = session.CreatedAt
	}
	return nil
}

// normalizeSessions upgrades schema-v1 session encodings in memory without
// changing the persisted schema version. This keeps old snapshots readable
// while ensuring all current code sees separate lifecycle and runtime phase.
func (s *State) normalizeSessions() {
	s.pruneSessionTombstones()
	for userID, preferences := range s.Preferences {
		preferences.normalize()
		s.Preferences[userID] = preferences
	}
	s.normalizeDeferredInputs()
	archivedLegacy := make([]SessionRef, 0)
	for key, session := range s.Sessions {
		node := s.Nodes[session.NodeID]
		switch session.State {
		case "":
			session.State = SessionLive
		case SessionIdle:
			session.State = SessionLive
			session.RuntimePhase = RuntimeIdle
		case SessionRecovering:
			session.State = SessionLive
			session.RuntimePhase = RuntimeDegraded
			session.ResumePending = true
		case SessionLost:
			if node.Status == NodeOffline || node.Status == NodeReconnecting {
				session.State = SessionLive
				session.RuntimePhase = RuntimeDegraded
			} else {
				session.State = SessionArchived
				session.ArchiveReason = ArchiveResumeFailed
				if session.ArchivedAt.IsZero() {
					session.ArchivedAt = legacyArchiveTime(session)
				}
				archivedLegacy = append(archivedLegacy, session.Ref())
			}
		}
		if session.RuntimePhase == "" {
			session.RuntimePhase = RuntimeIdle
		}
		if session.RuntimeGeneration == 0 {
			session.RuntimeGeneration = 1
		}
		if session.Revision == 0 {
			session.Revision = 1
		}
		if session.InteractivePrompt != nil {
			report := InteractivePromptReport{
				SessionID: session.ID, Generation: session.RuntimeGeneration, Present: true,
				Kind: session.InteractivePrompt.Kind, Hash: session.InteractivePrompt.Hash,
			}
			if session.RuntimePhase != RuntimeWaitingInput || report.validate() != nil {
				session.InteractivePrompt = nil
			}
		}
		if session.InteractivePrompt == nil && session.RuntimePhase == RuntimeWaitingInput {
			session.RuntimePhase = RuntimeRunning
		}
		if session.ProviderBindingSince.IsZero() {
			session.ProviderBindingSince = session.CreatedAt
		}
		if session.State != SessionLive {
			session.ResumePending = false
		}
		s.Sessions[key] = session
	}
	for _, ref := range archivedLegacy {
		s.repairNavigationAfterUnavailable(ref)
	}
}

func legacyArchiveTime(session Session) time.Time {
	if !session.LastEventAt.IsZero() {
		return session.LastEventAt
	}
	return session.CreatedAt
}
