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
	session.ArchiveDescription = nil
	session.DescriptionVersion = 0
	session.RuntimeIssue = ""
	session.VoiceAcknowledgements = nil
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
		if session.RuntimeIssue != RuntimeIssueProviderHookUnavailable ||
			session.RuntimePhase != RuntimeDegraded {
			session.RuntimeIssue = ""
		}
		session.VoiceAcknowledgements = normalizeVoiceAcknowledgements(session.VoiceAcknowledgements)
		if session.State == SessionArchived &&
			session.DescriptionVersion == ArchiveDescriptionVersion {
			if description, err := NormalizeArchiveDescription(session.ArchiveDescription); err == nil {
				session.ArchiveDescription = description
			} else {
				session.ArchiveDescription = nil
				session.DescriptionVersion = 0
			}
		} else {
			session.ArchiveDescription = nil
			session.DescriptionVersion = 0
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

func normalizeVoiceAcknowledgements(values []VoiceAcknowledgement) []VoiceAcknowledgement {
	result := make([]VoiceAcknowledgement, 0, min(len(values), maxVoiceAcknowledgements))
	seen := make(map[string]bool)
	for _, value := range values {
		if value.Ordinal == 0 {
			value.Ordinal = 1
		}
		if value.OperationID == "" || len(value.OperationID) > 128 || seen[value.OperationID] ||
			(value.Status != OperationQueued && value.Status != OperationSucceeded && value.Status != OperationFailed) ||
			value.AcceptedAt.IsZero() || value.UpdatedAt.Before(value.AcceptedAt) ||
			value.Ordinal < 1 || value.Ordinal > 16 ||
			value.BaselineCount < 0 || value.BaselineCount > 400 ||
			(!value.BaselineKnown && value.BaselineCount != 0) {
			continue
		}
		seen[value.OperationID] = true
		result = append(result, value)
	}
	if len(result) > maxVoiceAcknowledgements {
		result = append([]VoiceAcknowledgement(nil), result[len(result)-maxVoiceAcknowledgements:]...)
	}
	return result
}

func legacyArchiveTime(session Session) time.Time {
	if !session.LastEventAt.IsZero() {
		return session.LastEventAt
	}
	return session.CreatedAt
}
