package singlemachinecomposition

import (
	"strconv"
	"time"

	"bria/internal/app"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
)

// Persist only an allowlisted classification, never stderr or provider error
// text: startup diagnostics may contain credentials or private workspace data.
func recordSessionStartup(logger *safelog.Logger, intent app.ConfirmedSessionIntent, elapsed time.Duration, failure error) {
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
	_ = logger.Write(safelog.Event{Class: safelog.Service, Type: "session.startup", EntityID: string(intent.IntentID), Result: result, ErrorCategory: category,
		Fields: map[string]string{"provider": string(intent.Provider), "computer_id": string(intent.ComputerID), "duration_ms": strconv.FormatInt(elapsed.Milliseconds(), 10)}})
}
