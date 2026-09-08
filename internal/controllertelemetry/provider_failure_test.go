package controllertelemetry_test

import (
	"bria/internal/controllertelemetry"
	"testing"
)

func TestProviderFailureClassesSurviveSafeTelemetryBoundary(t *testing.T) {
	if controllertelemetry.SafeStage("session.provider_failure") != "session.provider_failure" {
		t.Error("provider failure stage is lost")
	}
	for _, class := range []string{"native_transcript_record_too_large", "native_transcript_read_limit", "native_transcript_malformed", "native_transcript_binding_invalid", "provider_failure"} {
		if controllertelemetry.SafeReason(class) != class {
			t.Errorf("safe runtime class %s is lost", class)
		}
		if controllertelemetry.RuntimeFailureReason(class).String() != class {
			t.Errorf("runtime mapping lost %s", class)
		}
	}
	for _, untrusted := range []string{"", "private-provider-payload", "native_transcript_malformed: private-detail", " native_transcript_read_limit", "NATIVE_TRANSCRIPT_MALFORMED"} {
		if controllertelemetry.RuntimeFailureReason(untrusted) != controllertelemetry.GenericProviderFailure {
			t.Fatal("non-exact runtime class escaped closed mapping")
		}
	}
	if controllertelemetry.ProviderFailure.String() != "session.provider_failure" {
		t.Fatal("typed failure stage differs from safe stage")
	}
}
