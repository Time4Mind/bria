package singlemachinecomposition

import (
	"strconv"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
)

// Persist only an allowlisted classification, never stderr or provider error
// text: startup diagnostics may contain credentials or private workspace data.
func recordSessionStartup(
	logger *safelog.Logger,
	intent app.ConfirmedSessionIntent,
	sessionID domain.SessionID,
	attempts int,
	elapsed time.Duration,
	failure error,
) {
	if logger == nil {
		return
	}
	result, category := "ready", ""
	if failure != nil {
		result, category = "failed", sessionruntime.StartupFailureClass(failure)
		if category == "" {
			category = "startup_failed"
		}
	}
	fields := map[string]string{
		"provider": string(intent.Provider), "computer_id": string(intent.ComputerID),
		"duration_ms": strconv.FormatInt(elapsed.Milliseconds(), 10), "attempt": strconv.Itoa(attempts),
	}
	if stage := sessionruntime.StartupFailureStage(failure); stage != "" {
		fields["stage"] = stage
	}
	_ = logger.Write(safelog.Event{Class: safelog.Service, Type: "session.startup", EntityID: string(sessionID), Result: result, ErrorCategory: category, Fields: fields})
}

func recordSessionStartupAttempt(logger *safelog.Logger, failure app.InitialStartAttemptFailure) {
	if logger == nil {
		return
	}
	category := sessionruntime.StartupFailureClass(failure.Err)
	if category == "" {
		category = "startup_failed"
	}
	fields := map[string]string{
		"provider": string(failure.Provider), "computer_id": string(failure.ComputerID), "attempt": strconv.Itoa(failure.Attempt),
	}
	if stage := sessionruntime.StartupFailureStage(failure.Err); stage != "" {
		fields["stage"] = stage
	}
	_ = logger.Write(safelog.Event{Class: safelog.Service, Type: "session.startup_attempt", EntityID: string(failure.SessionID), Result: "failed", ErrorCategory: category, Fields: fields})
}

func recordSessionRecoveryFailure(logger *safelog.Logger, sessionID domain.SessionID, failure error) {
	if logger == nil {
		return
	}
	category := sessionruntime.StartupFailureClass(failure)
	if category == "" {
		category = "session_recovery"
	}
	fields := map[string]string{"component": "sessionsupervisor", "operation": "recover"}
	if stage := sessionruntime.StartupFailureStage(failure); stage != "" {
		fields["stage"] = stage
	}
	_ = logger.Write(safelog.Event{
		Class: safelog.Critical, Type: "session.recovery_failed", EntityID: string(sessionID),
		Result: "failed", ErrorCategory: category, Fields: fields,
	})
}
