// Package telegramui projects application and domain state into semantic
// Telegram views. It does not choose transport copy, commands, or buttons.
package telegramui

import (
	"bria/internal/app"
	"bria/internal/domain"
)

// CreationPreview contains the product choices shown before a confirmed
// session creation is started.
type CreationPreview struct {
	Computer domain.ComputerID
	Provider domain.Provider
	Workdir  string
}

// SessionState is the lifecycle state visible in a session card. Its eventual
// Telegram wording belongs to a renderer once that product copy is specified.
type SessionState string

const (
	SessionStarting         SessionState = "starting"
	SessionResuming         SessionState = "resuming"
	SessionReady            SessionState = "ready"
	SessionRunning          SessionState = "running"
	SessionStopping         SessionState = "stopping"
	SessionClosingAfterWork SessionState = "closing_after_work"
	SessionAwaitingRecovery SessionState = "awaiting_recovery"
	SessionClosing          SessionState = "closing"
	SessionArchived         SessionState = "archived"
	SessionResumeFailed     SessionState = "resume_failed"
)

// SessionCard contains only the immutable product choices and lifecycle state
// needed for the current Stage 2 card.
type SessionCard struct {
	Computer domain.ComputerID
	Provider domain.Provider
	Workdir  string
	State    SessionState
}

// PreviewCreation projects an already confirmed intent without exposing its
// idempotency identity.
func PreviewCreation(intent app.ConfirmedSessionIntent) CreationPreview {
	return CreationPreview{
		Computer: intent.ComputerID,
		Provider: intent.Provider,
		Workdir:  intent.Workdir,
	}
}

// ProjectSessionCard projects a valid session without exposing Bria or
// provider session identifiers, process generation, binding, or errors.
func ProjectSessionCard(session domain.Session) SessionCard {
	return SessionCard{
		Computer: session.ComputerID(),
		Provider: session.Provider(),
		Workdir:  session.Workdir(),
		State:    SessionState(session.Status()),
	}
}
