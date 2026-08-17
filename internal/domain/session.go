package domain

import (
	"cmp"
	"slices"
	"time"
)

// SessionState is the durable lifecycle of a session. Runtime progress belongs
// in RuntimePhase and must never be encoded as a lifecycle state.
type SessionState string

const (
	SessionLive     SessionState = "active"
	SessionArchived SessionState = "archived"

	// SessionActive is retained as a source-compatible name for schema-v1
	// callers. New code should use SessionLive.
	SessionActive = SessionLive

	// These values can occur in schema-v1 snapshots. State normalization maps
	// them to SessionLive plus a RuntimePhase; new mutations never write them.
	SessionIdle       SessionState = "idle"
	SessionRecovering SessionState = "recovering"
	SessionLost       SessionState = "lost"
)

// RuntimePhase describes transient progress of a live session independently
// of its durable lifecycle.
type RuntimePhase string

const (
	RuntimeStarting     RuntimePhase = "starting"
	RuntimeIdle         RuntimePhase = "idle"
	RuntimeRunning      RuntimePhase = "running"
	RuntimeWaitingInput RuntimePhase = "waiting_input"
	RuntimeStopping     RuntimePhase = "stopping"
	RuntimeDegraded     RuntimePhase = "degraded"
)

type ArchiveReason string

const (
	ArchiveManual       ArchiveReason = "manual"
	ArchiveIdle         ArchiveReason = "idle"
	ArchiveNodeReboot   ArchiveReason = "node_reboot"
	ArchiveResumeFailed ArchiveReason = "resume_failed"
)

type OperationStatus string

const (
	OperationQueued    OperationStatus = "queued"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
)

// SessionOperationResult is intentionally small. Provider output and raw
// transcripts are node-local and must not be replicated through Raft.
type SessionOperationResult struct {
	OperationID string          `json:"operation_id"`
	Action      SessionAction   `json:"action"`
	Status      OperationStatus `json:"status"`
	Detail      string          `json:"detail,omitempty"`
	At          time.Time       `json:"at"`
}

type Session struct {
	ID                   SessionID               `json:"id"`
	NodeID               NodeID                  `json:"node_id"`
	OwnerID              UserID                  `json:"owner_id"`
	Name                 string                  `json:"name"`
	NameFormatVersion    int                     `json:"name_format_version,omitempty"`
	Workdir              string                  `json:"workdir"`
	Backend              string                  `json:"backend"`
	ProviderSessionID    string                  `json:"provider_session_id,omitempty"`
	ProviderResume       bool                    `json:"provider_resume,omitempty"`
	ProviderBindingSince time.Time               `json:"provider_binding_since,omitempty"`
	State                SessionState            `json:"state"`
	RuntimePhase         RuntimePhase            `json:"runtime_phase,omitempty"`
	RuntimeGeneration    uint64                  `json:"runtime_generation,omitempty"`
	Revision             uint64                  `json:"revision,omitempty"`
	ResumePending        bool                    `json:"resume_pending,omitempty"`
	InteractivePrompt    *InteractivePrompt      `json:"interactive_prompt,omitempty"`
	LastOperation        *SessionOperationResult `json:"last_operation,omitempty"`
	CreatedAt            time.Time               `json:"created_at"`
	LiveSinceAt          time.Time               `json:"live_since_at"`
	LastEventAt          time.Time               `json:"last_event_at"`
	ArchivedAt           time.Time               `json:"archived_at,omitempty"`
	ArchiveID            string                  `json:"archive_id,omitempty"`
	ArchiveReady         bool                    `json:"archive_ready,omitempty"`
	RestoredAt           time.Time               `json:"restored_at,omitempty"`
	ArchiveReason        ArchiveReason           `json:"archive_reason,omitempty"`
}

func (s Session) Ref() SessionRef {
	return SessionRef{NodeID: s.NodeID, SessionID: s.ID}
}

func (s Session) IsLive() bool {
	return s.State == SessionLive
}

func validRuntimePhase(phase RuntimePhase) bool {
	switch phase {
	case RuntimeStarting, RuntimeIdle, RuntimeRunning, RuntimeWaitingInput, RuntimeStopping, RuntimeDegraded:
		return true
	default:
		return false
	}
}

func SortLive(sessions []Session) {
	slices.SortFunc(sessions, func(a, b Session) int {
		aTime := a.LiveSinceAt
		if aTime.IsZero() {
			aTime = a.CreatedAt
		}
		bTime := b.LiveSinceAt
		if bTime.IsZero() {
			bTime = b.CreatedAt
		}
		if order := aTime.Compare(bTime); order != 0 {
			return order
		}
		if order := cmp.Compare(a.NodeID, b.NodeID); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func SortArchived(sessions []Session) {
	slices.SortFunc(sessions, func(a, b Session) int {
		aTime := archiveSortTime(a)
		bTime := archiveSortTime(b)
		if order := bTime.Compare(aTime); order != 0 {
			return order
		}
		if order := cmp.Compare(a.NodeID, b.NodeID); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func archiveSortTime(session Session) time.Time {
	if !session.ArchivedAt.IsZero() {
		return session.ArchivedAt
	}
	if !session.LastEventAt.IsZero() {
		return session.LastEventAt
	}
	return session.CreatedAt
}
