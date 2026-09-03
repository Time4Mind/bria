package domain

import (
	"fmt"
	"strings"
	"time"
)

// SessionID identifies one logical Bria session.
type SessionID string

// IntentID is the caller-supplied idempotency key for one confirmed creation.
type IntentID string

// ComputerID identifies the computer that owns and runs a session.
type ComputerID string

// Provider identifies the agent implementation bound to a session.
type Provider string

const (
	ProviderCodex  Provider = "codex"
	ProviderClaude Provider = "claude"
)

func ValidateSessionLifetime(lifetime SessionLifetime) error {
	_, err := deadlineFrom(time.Unix(0, 0).UTC(), lifetime)
	return err
}

// SessionStatus is the persisted lifecycle state of a logical session.
type SessionStatus string

const (
	SessionStarting         SessionStatus = "starting"
	SessionResuming         SessionStatus = "resuming"
	SessionReady            SessionStatus = "ready"
	SessionRunning          SessionStatus = "running"
	SessionStopping         SessionStatus = "stopping"
	SessionClosingAfterWork SessionStatus = "closing_after_work"
	SessionAwaitingRecovery SessionStatus = "awaiting_recovery"
	SessionClosing          SessionStatus = "closing"
	SessionArchived         SessionStatus = "archived"
	SessionResumeFailed     SessionStatus = "resume_failed"
)

// SessionLifetime is the global lifetime applied when a session is created or
// successfully resumed. Zero means that automatic closing is disabled.
type SessionLifetime time.Duration

const (
	SessionLifetimeNever   SessionLifetime = 0
	SessionLifetime6Hours  SessionLifetime = SessionLifetime(6 * time.Hour)
	SessionLifetime12Hours SessionLifetime = SessionLifetime(12 * time.Hour)
	SessionLifetime24Hours SessionLifetime = SessionLifetime(24 * time.Hour)
	SessionLifetime48Hours SessionLifetime = SessionLifetime(48 * time.Hour)
)

// ProviderBinding identifies the concrete provider session and process
// generation created for a logical Bria session.
type ProviderBinding struct {
	Provider   Provider
	SessionID  string
	Generation uint64
}

// Session is an immutable logical Bria session. State transitions return a new
// value and never change its computer, provider, or working directory.
type Session struct {
	id             SessionID
	intentID       IntentID
	computerID     ComputerID
	provider       Provider
	workdir        string
	status         SessionStatus
	binding        *ProviderBinding
	createdAt      time.Time
	lastResumedAt  *time.Time
	stateChangedAt time.Time
	lifetime       SessionLifetime
	deadlineAt     *time.Time
	recoveryTarget *SessionStatus
}

// SessionSnapshot is the persistence representation of a Session.
type SessionSnapshot struct {
	ID             SessionID
	IntentID       IntentID
	ComputerID     ComputerID
	Provider       Provider
	Workdir        string
	Status         SessionStatus
	Binding        *ProviderBinding
	CreatedAt      time.Time
	LastResumedAt  *time.Time
	StateChangedAt time.Time
	Lifetime       SessionLifetime
	DeadlineAt     *time.Time
	RecoveryTarget *SessionStatus
}

func NewStartingSession(
	id SessionID,
	intentID IntentID,
	computerID ComputerID,
	provider Provider,
	workdir string,
) (Session, error) {
	return NewStartingSessionAt(id, intentID, computerID, provider, workdir, time.Now().UTC(), SessionLifetimeNever)
}

// NewStartingSessionAt creates a new logical provider session and fixes the
// lifetime anchor before any provider process is launched.
func NewStartingSessionAt(
	id SessionID,
	intentID IntentID,
	computerID ComputerID,
	provider Provider,
	workdir string,
	createdAt time.Time,
	lifetime SessionLifetime,
) (Session, error) {
	if err := ValidateSessionIntent(intentID, computerID, provider, workdir); err != nil {
		return Session{}, err
	}
	createdAt, err := validateLifecycleTime("created at", createdAt)
	if err != nil {
		return Session{}, err
	}
	deadline, err := deadlineFrom(createdAt, lifetime)
	if err != nil {
		return Session{}, err
	}
	return RestoreSession(SessionSnapshot{
		ID:             id,
		IntentID:       intentID,
		ComputerID:     computerID,
		Provider:       provider,
		Workdir:        workdir,
		Status:         SessionStarting,
		CreatedAt:      createdAt,
		StateChangedAt: createdAt,
		Lifetime:       lifetime,
		DeadlineAt:     deadline,
	})
}

// ValidateSessionIntent checks the immutable choices before an ID is allocated
// or any durable/external operation begins.
func ValidateSessionIntent(
	intentID IntentID,
	computerID ComputerID,
	provider Provider,
	workdir string,
) error {
	if strings.TrimSpace(string(intentID)) == "" {
		return fmt.Errorf("intent id is required")
	}
	if strings.TrimSpace(string(computerID)) == "" {
		return fmt.Errorf("computer id is required")
	}
	if provider != ProviderCodex && provider != ProviderClaude {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	if strings.TrimSpace(workdir) == "" {
		return fmt.Errorf("workdir is required")
	}
	return nil
}

func RestoreSession(snapshot SessionSnapshot) (Session, error) {
	if strings.TrimSpace(string(snapshot.ID)) == "" {
		return Session{}, fmt.Errorf("session id is required")
	}
	if err := ValidateSessionIntent(
		snapshot.IntentID,
		snapshot.ComputerID,
		snapshot.Provider,
		snapshot.Workdir,
	); err != nil {
		return Session{}, err
	}

	// Older single-computer snapshots did not persist the recovery target. A
	// binding-less awaiting session could only have failed its initial start;
	// one with a binding had already reached provider readiness.
	if snapshot.Status == SessionAwaitingRecovery && snapshot.RecoveryTarget == nil {
		target := SessionStarting
		if snapshot.Binding != nil {
			target = SessionReady
		}
		snapshot.RecoveryTarget = &target
	}

	if err := validateLifecycleSnapshot(snapshot); err != nil {
		return Session{}, err
	}

	switch snapshot.Status {
	case SessionStarting:
		if snapshot.Binding != nil {
			return Session{}, fmt.Errorf("status %q cannot have a provider binding", snapshot.Status)
		}
	case SessionResuming, SessionReady, SessionRunning, SessionStopping,
		SessionClosingAfterWork, SessionClosing, SessionArchived, SessionResumeFailed:
		if err := validateBinding(snapshot.Provider, snapshot.Binding); err != nil {
			return Session{}, err
		}
	case SessionAwaitingRecovery:
		if snapshot.Binding != nil {
			if err := validateBinding(snapshot.Provider, snapshot.Binding); err != nil {
				return Session{}, err
			}
		}
		if snapshot.RecoveryTarget == nil {
			return Session{}, errorsForStatus(snapshot.Status, "requires a recovery target")
		}
		if !isRecoveryTarget(*snapshot.RecoveryTarget) {
			return Session{}, errorsForStatus(snapshot.Status, "has an invalid recovery target")
		}
		if *snapshot.RecoveryTarget == SessionStarting && snapshot.Binding != nil {
			return Session{}, errorsForStatus(snapshot.Status, "cannot retain a binding for initial start recovery")
		}
		if *snapshot.RecoveryTarget != SessionStarting && snapshot.Binding == nil {
			return Session{}, errorsForStatus(snapshot.Status, "requires the prior provider binding")
		}
	default:
		return Session{}, fmt.Errorf("unsupported session status %q", snapshot.Status)
	}
	if snapshot.Status != SessionAwaitingRecovery && snapshot.RecoveryTarget != nil {
		return Session{}, errorsForStatus(snapshot.Status, "cannot have a recovery target")
	}

	return Session{
		id:             snapshot.ID,
		intentID:       snapshot.IntentID,
		computerID:     snapshot.ComputerID,
		provider:       snapshot.Provider,
		workdir:        snapshot.Workdir,
		status:         snapshot.Status,
		binding:        cloneBinding(snapshot.Binding),
		createdAt:      snapshot.CreatedAt,
		lastResumedAt:  cloneTime(snapshot.LastResumedAt),
		stateChangedAt: snapshot.StateChangedAt,
		lifetime:       snapshot.Lifetime,
		deadlineAt:     cloneTime(snapshot.DeadlineAt),
		recoveryTarget: cloneStatus(snapshot.RecoveryTarget),
	}, nil
}

func (s Session) Ready(binding ProviderBinding) (Session, error) {
	return s.ReadyAt(binding, time.Now().UTC())
}

func (s Session) ReadyAt(binding ProviderBinding, at time.Time) (Session, error) {
	if s.status != SessionStarting {
		return Session{}, fmt.Errorf("cannot mark session %q ready from %q", s.id, s.status)
	}
	if err := validateBinding(s.provider, &binding); err != nil {
		return Session{}, err
	}

	next, err := s.transition(SessionReady, at)
	if err != nil {
		return Session{}, err
	}
	next.binding = cloneBinding(&binding)
	return next, nil
}

func (s Session) AwaitRecovery() (Session, error) {
	return s.AwaitRecoveryAt(time.Now().UTC())
}

func (s Session) AwaitRecoveryAt(at time.Time) (Session, error) {
	switch s.status {
	case SessionStarting, SessionResuming, SessionReady, SessionRunning, SessionStopping,
		SessionClosingAfterWork, SessionClosing:
	default:
		return Session{}, fmt.Errorf("cannot mark session %q awaiting recovery from %q", s.id, s.status)
	}
	previous := s.status
	next, err := s.transition(SessionAwaitingRecovery, at)
	if err != nil {
		return Session{}, err
	}
	next.status = SessionAwaitingRecovery
	next.recoveryTarget = &previous
	return next, nil
}

func (s Session) StartWork(at time.Time) (Session, error) {
	if s.status != SessionReady {
		return Session{}, fmt.Errorf("cannot start work for session %q from %q", s.id, s.status)
	}
	return s.transition(SessionRunning, at)
}

func (s Session) BeginStop(at time.Time) (Session, error) {
	if s.status != SessionRunning {
		return Session{}, fmt.Errorf("cannot stop work for session %q from %q", s.id, s.status)
	}
	return s.transition(SessionStopping, at)
}

func (s Session) FinishWork(at time.Time) (Session, error) {
	if s.status != SessionRunning && s.status != SessionStopping {
		return Session{}, fmt.Errorf("cannot finish work for session %q from %q", s.id, s.status)
	}
	return s.transition(SessionReady, at)
}

func (s Session) CloseAfterWork(at time.Time) (Session, error) {
	if s.status != SessionRunning && s.status != SessionStopping {
		return Session{}, fmt.Errorf("cannot schedule close for session %q from %q", s.id, s.status)
	}
	return s.transition(SessionClosingAfterWork, at)
}

func (s Session) BeginClose(at time.Time) (Session, error) {
	if s.status != SessionReady && s.status != SessionClosingAfterWork {
		return Session{}, fmt.Errorf("cannot close session %q from %q", s.id, s.status)
	}
	return s.transition(SessionClosing, at)
}

func (s Session) Archive(at time.Time) (Session, error) {
	if s.status != SessionClosing {
		return Session{}, fmt.Errorf("cannot archive session %q from %q", s.id, s.status)
	}
	next, err := s.transition(SessionArchived, at)
	if err != nil {
		return Session{}, err
	}
	next.deadlineAt = nil
	return next, nil
}

func (s Session) BeginResume(at time.Time) (Session, error) {
	if s.status != SessionArchived {
		return Session{}, fmt.Errorf("cannot resume session %q from %q", s.id, s.status)
	}
	return s.transition(SessionResuming, at)
}

func (s Session) ResumeReady(binding ProviderBinding, at time.Time, lifetime SessionLifetime) (Session, error) {
	if s.status != SessionResuming {
		return Session{}, fmt.Errorf("cannot finish resume for session %q from %q", s.id, s.status)
	}
	if err := validateResumedBinding(s.provider, s.binding, binding); err != nil {
		return Session{}, err
	}
	at, err := validateLifecycleTime("resumed at", at)
	if err != nil {
		return Session{}, err
	}
	deadline, err := deadlineFrom(at, lifetime)
	if err != nil {
		return Session{}, err
	}
	next, err := s.transition(SessionReady, at)
	if err != nil {
		return Session{}, err
	}
	next.binding = cloneBinding(&binding)
	next.lastResumedAt = &at
	next.lifetime = lifetime
	next.deadlineAt = deadline
	return next, nil
}

func (s Session) FailResume(at time.Time) (Session, error) {
	if s.status != SessionResuming {
		return Session{}, fmt.Errorf("cannot fail resume for session %q from %q", s.id, s.status)
	}
	return s.transition(SessionResumeFailed, at)
}

func (s Session) ReturnToArchive(at time.Time) (Session, error) {
	if s.status != SessionResumeFailed {
		return Session{}, fmt.Errorf("cannot return session %q to archive from %q", s.id, s.status)
	}
	return s.transition(SessionArchived, at)
}

func (s Session) Recovered(binding ProviderBinding, at time.Time) (Session, error) {
	if s.status != SessionAwaitingRecovery || s.recoveryTarget == nil {
		return Session{}, fmt.Errorf("cannot recover session %q from %q", s.id, s.status)
	}
	if s.binding == nil {
		if *s.recoveryTarget != SessionStarting {
			return Session{}, errorsForStatus(s.status, "binding-less recovery is only valid for a new session")
		}
		if err := validateBinding(s.provider, &binding); err != nil {
			return Session{}, err
		}
	} else if err := validateResumedBinding(s.provider, s.binding, binding); err != nil {
		return Session{}, err
	}
	target := *s.recoveryTarget
	if target == SessionStarting || target == SessionResuming ||
		target == SessionRunning || target == SessionStopping {
		target = SessionReady
	}
	next, err := s.transition(target, at)
	if err != nil {
		return Session{}, err
	}
	next.binding = cloneBinding(&binding)
	next.recoveryTarget = nil
	if *s.recoveryTarget == SessionResuming {
		resumedAt := next.stateChangedAt
		next.lastResumedAt = &resumedAt
		deadline, deadlineErr := deadlineFrom(resumedAt, next.lifetime)
		if deadlineErr != nil {
			return Session{}, deadlineErr
		}
		next.deadlineAt = deadline
	}
	return next, nil
}

func (s Session) ID() SessionID             { return s.id }
func (s Session) IntentID() IntentID        { return s.intentID }
func (s Session) ComputerID() ComputerID    { return s.computerID }
func (s Session) Provider() Provider        { return s.provider }
func (s Session) Workdir() string           { return s.workdir }
func (s Session) Status() SessionStatus     { return s.status }
func (s Session) CreatedAt() time.Time      { return s.createdAt }
func (s Session) StateChangedAt() time.Time { return s.stateChangedAt }
func (s Session) Lifetime() SessionLifetime { return s.lifetime }
func (s Session) LastResumedAt() (time.Time, bool) {
	if s.lastResumedAt == nil {
		return time.Time{}, false
	}
	return *s.lastResumedAt, true
}
func (s Session) Deadline() (time.Time, bool) {
	if s.deadlineAt == nil {
		return time.Time{}, false
	}
	return *s.deadlineAt, true
}
func (s Session) RecoveryTarget() (SessionStatus, bool) {
	if s.recoveryTarget == nil {
		return "", false
	}
	return *s.recoveryTarget, true
}
func (s Session) Expired(now time.Time) bool {
	return s.deadlineAt != nil && !now.Before(*s.deadlineAt)
}

// SessionDeadline returns the fixed automatic-close deadline for one lifetime
// anchor. The boolean is false only for the supported "never" value.
func SessionDeadline(anchor time.Time, lifetime SessionLifetime) (time.Time, bool, error) {
	anchor, err := validateLifecycleTime("lifetime anchor", anchor)
	if err != nil {
		return time.Time{}, false, err
	}
	deadline, err := deadlineFrom(anchor, lifetime)
	if err != nil {
		return time.Time{}, false, err
	}
	if deadline == nil {
		return time.Time{}, false, nil
	}
	return *deadline, true, nil
}
func (s Session) Snapshot() SessionSnapshot {
	return SessionSnapshot{
		ID:             s.id,
		IntentID:       s.intentID,
		ComputerID:     s.computerID,
		Provider:       s.provider,
		Workdir:        s.workdir,
		Status:         s.status,
		Binding:        cloneBinding(s.binding),
		CreatedAt:      s.createdAt,
		LastResumedAt:  cloneTime(s.lastResumedAt),
		StateChangedAt: s.stateChangedAt,
		Lifetime:       s.lifetime,
		DeadlineAt:     cloneTime(s.deadlineAt),
		RecoveryTarget: cloneStatus(s.recoveryTarget),
	}
}

func (s Session) Binding() (ProviderBinding, bool) {
	if s.binding == nil {
		return ProviderBinding{}, false
	}
	return *cloneBinding(s.binding), true
}

// Equal reports whether two values describe the same logical session and state.
func (s Session) Equal(other Session) bool {
	if s.id != other.id ||
		s.intentID != other.intentID ||
		s.computerID != other.computerID ||
		s.provider != other.provider ||
		s.workdir != other.workdir ||
		s.status != other.status ||
		!s.createdAt.Equal(other.createdAt) ||
		!equalTime(s.lastResumedAt, other.lastResumedAt) ||
		!s.stateChangedAt.Equal(other.stateChangedAt) ||
		s.lifetime != other.lifetime ||
		!equalTime(s.deadlineAt, other.deadlineAt) ||
		!equalStatus(s.recoveryTarget, other.recoveryTarget) {
		return false
	}
	if s.binding == nil || other.binding == nil {
		return s.binding == nil && other.binding == nil
	}
	return *s.binding == *other.binding
}

func (s Session) transition(status SessionStatus, at time.Time) (Session, error) {
	at, err := validateLifecycleTime("state changed at", at)
	if err != nil {
		return Session{}, err
	}
	if !s.stateChangedAt.IsZero() && at.Before(s.stateChangedAt) {
		return Session{}, fmt.Errorf("state change time %s precedes prior change %s", at, s.stateChangedAt)
	}
	next := s
	next.status = status
	next.stateChangedAt = at
	next.recoveryTarget = nil
	return next, nil
}

func deadlineFrom(anchor time.Time, lifetime SessionLifetime) (*time.Time, error) {
	switch lifetime {
	case SessionLifetimeNever:
		return nil, nil
	case SessionLifetime6Hours, SessionLifetime12Hours, SessionLifetime24Hours, SessionLifetime48Hours:
		deadline := anchor.Add(time.Duration(lifetime))
		return &deadline, nil
	default:
		return nil, fmt.Errorf("unsupported session lifetime %s", time.Duration(lifetime))
	}
}

func validateLifecycleSnapshot(snapshot SessionSnapshot) error {
	if snapshot.CreatedAt.IsZero() && snapshot.StateChangedAt.IsZero() {
		if snapshot.LastResumedAt != nil || snapshot.DeadlineAt != nil || snapshot.Lifetime != SessionLifetimeNever {
			return errorsForStatus(snapshot.Status, "legacy timestamps cannot contain lifecycle timing fields")
		}
		return nil
	}
	created, err := validateLifecycleTime("created at", snapshot.CreatedAt)
	if err != nil {
		return err
	}
	changed, err := validateLifecycleTime("state changed at", snapshot.StateChangedAt)
	if err != nil {
		return err
	}
	if changed.Before(created) {
		return errorsForStatus(snapshot.Status, "state change precedes creation")
	}
	if _, err := deadlineFrom(created, snapshot.Lifetime); err != nil {
		return err
	}
	if snapshot.LastResumedAt != nil && snapshot.LastResumedAt.Before(created) {
		return errorsForStatus(snapshot.Status, "resume precedes creation")
	}
	if snapshot.LastResumedAt != nil && snapshot.LastResumedAt.After(changed) {
		return errorsForStatus(snapshot.Status, "resume follows the latest state change")
	}
	anchor := created
	if snapshot.LastResumedAt != nil {
		anchor = snapshot.LastResumedAt.UTC()
	}
	wantDeadline, err := deadlineFrom(anchor, snapshot.Lifetime)
	if err != nil {
		return err
	}
	if !equalTime(snapshot.DeadlineAt, wantDeadline) &&
		snapshot.Status != SessionArchived && snapshot.Status != SessionResuming && snapshot.Status != SessionResumeFailed {
		return errorsForStatus(snapshot.Status, "deadline does not match its creation or resume anchor")
	}
	return nil
}

func isRecoveryTarget(status SessionStatus) bool {
	switch status {
	case SessionStarting, SessionResuming, SessionReady, SessionRunning, SessionStopping,
		SessionClosingAfterWork, SessionClosing:
		return true
	default:
		return false
	}
}

func validateLifecycleTime(name string, value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	return value.UTC(), nil
}

func validateResumedBinding(provider Provider, prior *ProviderBinding, next ProviderBinding) error {
	if err := validateBinding(provider, &next); err != nil {
		return err
	}
	if prior == nil {
		return fmt.Errorf("prior provider binding is required for exact resume")
	}
	if next.SessionID != prior.SessionID {
		return fmt.Errorf("resumed provider session id %q does not match original %q", next.SessionID, prior.SessionID)
	}
	if next.Generation <= prior.Generation {
		return fmt.Errorf("resumed provider generation %d must be greater than prior generation %d", next.Generation, prior.Generation)
	}
	return nil
}

func errorsForStatus(status SessionStatus, message string) error {
	return fmt.Errorf("status %q %s", status, message)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStatus(value *SessionStatus) *SessionStatus {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func equalStatus(left, right *SessionStatus) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateBinding(provider Provider, binding *ProviderBinding) error {
	if binding == nil {
		return fmt.Errorf("ready session requires a provider binding")
	}
	if binding.Provider != provider {
		return fmt.Errorf("provider binding %q does not match session provider %q", binding.Provider, provider)
	}
	if strings.TrimSpace(binding.SessionID) == "" {
		return fmt.Errorf("provider session id is required")
	}
	if binding.Generation == 0 {
		return fmt.Errorf("provider launch generation must be greater than zero")
	}
	return nil
}

func cloneBinding(binding *ProviderBinding) *ProviderBinding {
	if binding == nil {
		return nil
	}
	copy := *binding
	return &copy
}
