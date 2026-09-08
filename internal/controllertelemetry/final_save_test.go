package controllertelemetry_test

import (
	"testing"

	"bria/internal/controllertelemetry"
)

func TestFinalSaveSafeVocabulary(t *testing.T) {
	if controllertelemetry.FinalSave.String() != "session.final_save" || controllertelemetry.ProviderFailure.String() != "session.provider_failure" {
		t.Fatal("final persistence must be distinct from provider failure")
	}
	if got := controllertelemetry.SafeStage("session.final_save"); got != "session.final_save" {
		t.Fatalf("final persistence stage lost: %q", got)
	}
	if controllertelemetry.PersistFailed.String() != "persist_failed" || controllertelemetry.Failed.String() != "failed" || controllertelemetry.Persisted.String() != "persisted" {
		t.Fatal("final save must reuse existing persistence reason and outcomes")
	}
	for _, unsafe := range []string{"session.final_save token=private", "session.final_save.failed", "SESSION.FINAL_SAVE", " final_save"} {
		if controllertelemetry.SafeStage(unsafe) != "unknown" {
			t.Fatal("stage accepted arbitrary error or payload text")
		}
	}
}
