package processlog

import (
	"fmt"
)

type FailureClass string

const (
	FailureNone         FailureClass = "none"
	FailureTransient    FailureClass = "transient"
	FailureTimeout      FailureClass = "timeout"
	FailureCancelled    FailureClass = "cancelled"
	FailureRateLimited  FailureClass = "rate_limited"
	FailureAvailability FailureClass = "availability"
	FailureStaleState   FailureClass = "stale_state"
	FailureInvalidState FailureClass = "invalid_state"
	FailurePermission   FailureClass = "permission"
	FailureNotFound     FailureClass = "not_found"
	FailureIO           FailureClass = "io"
	FailureConsistency  FailureClass = "consistency"
	FailureTransport    FailureClass = "transport"
	FailureDependency   FailureClass = "dependency"
	FailureInternal     FailureClass = "internal"
	FailureUnclassified FailureClass = "unclassified"
)

// Failuref emits a structured record with an explicit allowlisted failure
// class. Use it where retry/terminal semantics cannot be inferred from text.
func Failuref(level Level, class FailureClass, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	_, _ = writeStructured(level, normalizeFailureClass(class), message)
}

// Outcomef classifies one allowlisted outcome token without parsing a rendered
// log line. Unknown outcomes fail closed as unclassified.
func Outcomef(level Level, outcome string, format string, args ...any) {
	Failuref(level, FailureClassForOutcome(outcome), format, args...)
}

func FailureClassForOutcome(outcome string) FailureClass {
	switch outcome {
	case "ok", "processed", "recovered", "delivered", "ready", "attached", "changed",
		"stable_cache", "stale_cache", "verified_cache", "verified_png", "initial_ready",
		"no_active_card", "background_settled", "inactive_session", "not_visible",
		"already_delivered", "ignored", "terminal", "skipped_current":
		return FailureNone
	case "retry_scheduled", "waiting", "settlement_pending", "final_pending",
		"runtime_pending", "card_unavailable", "delivery_pending":
		return FailureTransient
	case "timeout", "foreground_timeout", "tmux_timeout", "queue_timeout", "deadline":
		return FailureTimeout
	case "cancelled":
		return FailureCancelled
	case "rate_limited":
		return FailureRateLimited
	case "runtime_unavailable", "target_missing", "controls_unavailable", "not_live",
		"resume_failed", "leadership_lost", "register_failed", "watchdog_handoff":
		return FailureAvailability
	case "stale_generation", "superseded", "stale_signal":
		return FailureStaleState
	case "validation_failed", "identity_failed", "queue_error", "invalid":
		return FailureInvalidState
	case "archive_verify_failed":
		return FailureIO
	case "raft_complete_failed", "runtime_failure_commit_failed", "restore_apply_failed",
		"select_apply_failed":
		return FailureConsistency
	case "callback_ack_failed", "initial_edit_failed", "settled_edit_failed":
		return FailureTransport
	case "partial":
		return FailureDependency
	case "failed", "error", "resolve_failed", "tmux_send_failed", "runtime_failed",
		"tmux_resume_failed", "prepare_failed", "control_failed", "initial_render_failed",
		"settled_render_failed", "render_error", "projection_error", "session_error",
		"lookup_failed", "transcript_error", "capture_error", "not_completed":
		return FailureInternal
	default:
		return FailureUnclassified
	}
}

func normalizeFailureClass(class FailureClass) FailureClass {
	switch class {
	case FailureNone, FailureTransient, FailureTimeout, FailureCancelled, FailureRateLimited,
		FailureAvailability, FailureStaleState, FailureInvalidState,
		FailurePermission, FailureNotFound, FailureIO, FailureConsistency,
		FailureTransport, FailureDependency, FailureInternal, FailureUnclassified:
		return class
	default:
		return FailureUnclassified
	}
}

func classifyFailure(level Level, _ string) FailureClass {
	if level == Critical {
		return FailureUnclassified
	}
	return FailureNone
}

func normalizedLevel(level Level) Level {
	switch level {
	case Detail, Service, Critical:
		return level
	default:
		return Service
	}
}
