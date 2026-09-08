package controllertelemetry_test

import (
	"bria/internal/controllertelemetry"
	"testing"
)

func TestRecoveryEnumsRoundTripAndRejectPayload(t *testing.T) {
	if controllertelemetry.RecoveryOutcome.String() != "session.recovery_outcome" || controllertelemetry.SafeStage("session.recovery_outcome") != "session.recovery_outcome" {
		t.Fatal("recovery stage not allowlisted")
	}
	for _, outcome := range []controllertelemetry.Outcome{controllertelemetry.Recovered, controllertelemetry.AwaitingRecovery, controllertelemetry.RecoveryUnknown, controllertelemetry.RecoveryExhausted} {
		if outcome.String() == "unknown" || controllertelemetry.SafeOutcome(outcome.String()) != outcome.String() {
			t.Fatal("recovery outcome lost")
		}
	}
	for _, reason := range []controllertelemetry.Reason{controllertelemetry.StartupRecovery, controllertelemetry.LiveRecovery, controllertelemetry.ManualRecovery} {
		if reason.String() == "unknown" || controllertelemetry.SafeReason(reason.String()) != reason.String() {
			t.Fatal("recovery source lost")
		}
	}
	for _, safe := range []func(string) string{controllertelemetry.SafeStage, controllertelemetry.SafeOutcome, controllertelemetry.SafeReason} {
		if safe("private-provider-error-payload") != "unknown" {
			t.Fatal("untyped payload accepted")
		}
	}
}
