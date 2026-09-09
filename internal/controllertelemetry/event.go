// Package controllertelemetry defines payload-free controller diagnostics.
// Identities exist only in memory; the logging adapter must HMAC them before I/O.
package controllertelemetry

import (
	"context"
	"time"
)

type Stage uint8
type Reason uint8
type Outcome uint8

const (
	StageUnknown Stage = iota
	ArchiveOutcome
	FallbackChoice
	SelectionPersist
	ProjectedTarget
	RecoveryOutcome
	ProviderFailure
	FinalSave
	NativeApproval
)

const (
	OutcomeUnknown Outcome = iota
	Scheduled
	Archived
	Deleted
	Selected
	Cleared
	Preserved
	Persisted
	Projected
	Skipped
	Failed
	Recovered
	AwaitingRecovery
	RecoveryUnknown
	RecoveryExhausted
	ApprovalConfirmed
	ApprovalStale
	ApprovalUncertain
)

const (
	ReasonUnknown Reason = iota
	ImmediateClose
	ScheduledClose
	ManualSelection
	RecentSelectable
	DurableSelectable
	NoSelectable
	NewerSelection
	OtherNode
	StaleSelection
	SessionCard
	SessionList
	NativeSurface
	CloseFailed
	InvalidCloseResult
	LoadFailed
	ListFailed
	PersistFailed
	ProjectionFailed
	StoreUnavailable
	Cancelled
	DeadlineExceeded
	StartupRecovery
	LiveRecovery
	ManualRecovery
	NativeTranscriptRecordTooLarge
	NativeTranscriptReadLimit
	NativeTranscriptMalformed
	NativeTranscriptBindingInvalid
	GenericProviderFailure
)

// Event contains facts observed at a controller boundary, not delivery receipts.
// Persisted requires a successful store call; Projected means a built projection.
// CandidateCount counts selectable same-node alternatives excluding SessionID;
// it is omitted from logs unless CandidatesKnown denotes a complete search.
// Empty TargetSessionID means no session for clear/list events, not a raw label.
type Event struct {
	Time                time.Time
	Stage               Stage
	Reason              Reason
	Outcome             Outcome
	OperationID         string
	ParentOperationID   string
	SessionID           string
	NodeID              string
	PreviousSessionID   string
	TargetSessionID     string
	ProviderSessionID   string
	ApprovalFingerprint string
	Generation          uint64
	CandidatesKnown     bool
	CandidateCount      uint64
}

// Observer must be non-blocking and must not affect controller success/failure.
type Observer interface {
	ObserveControllerEvent(context.Context, Event)
}

type operationKey struct{}

// WithOperation carries correlation only. It does not grant authorization.
// A deferred task must explicitly retain its initiating operation context.
func WithOperation(ctx context.Context, operationID string) context.Context {
	return context.WithValue(ctx, operationKey{}, operationID)
}

func Operation(ctx context.Context) string {
	value, _ := ctx.Value(operationKey{}).(string)
	return value
}

var stages = [...]string{"unknown", "session.archive_outcome", "selection.fallback", "selection.persist", "selection.project", "session.recovery_outcome", "session.provider_failure", "session.final_save", "native.approval"}
var outcomes = [...]string{"unknown", "scheduled", "archived", "deleted", "selected", "cleared", "preserved", "persisted", "projected", "skipped", "failed", "recovered", "awaiting_recovery", "recovery_unknown", "recovery_exhausted", "confirmed", "stale", "uncertain"}
var reasons = [...]string{
	"unknown", "immediate_close", "scheduled_close", "manual_selection", "recent_selectable", "durable_selectable",
	"no_selectable", "newer_selection", "other_node", "stale_selection", "session_card", "session_list", "native_surface",
	"close_failed", "invalid_close_result", "load_failed", "list_failed", "persist_failed", "projection_failed",
	"store_unavailable", "cancelled", "deadline_exceeded",
	"startup_recovery", "live_recovery", "manual_recovery",
	"native_transcript_record_too_large", "native_transcript_read_limit", "native_transcript_malformed", "native_transcript_binding_invalid", "provider_failure",
}

// RuntimeFailureReason accepts only exact runtime classes, never error text.
// Unrecognized input is reduced to a generic failure, not retained in the event.
func RuntimeFailureReason(class string) Reason {
	switch class {
	case "native_transcript_record_too_large":
		return NativeTranscriptRecordTooLarge
	case "native_transcript_read_limit":
		return NativeTranscriptReadLimit
	case "native_transcript_malformed":
		return NativeTranscriptMalformed
	case "native_transcript_binding_invalid":
		return NativeTranscriptBindingInvalid
	default:
		return GenericProviderFailure
	}
}

func (value Stage) String() string   { return enumString(uint8(value), stages[:]) }
func (value Outcome) String() string { return enumString(uint8(value), outcomes[:]) }
func (value Reason) String() string  { return enumString(uint8(value), reasons[:]) }

// Safe functions validate direct safe-logger callers as well as typed adapters.
func SafeStage(value string) string   { return allowed(value, stages[:]) }
func SafeOutcome(value string) string { return allowed(value, outcomes[:]) }
func SafeReason(value string) string  { return allowed(value, reasons[:]) }

func enumString(value uint8, values []string) string {
	if int(value) < len(values) {
		return values[value]
	}
	return "unknown"
}

func allowed(value string, values []string) string {
	for _, candidate := range values {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}
