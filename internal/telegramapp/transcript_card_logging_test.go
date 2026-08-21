package telegramapp

import (
	"testing"
	"time"
)

func TestCardTimingLoggingSuppressesOnlyFastSuccess(t *testing.T) {
	fast := sessionCardTiming{outcome: "ok", pane: paneAttachTiming{outcome: "skipped"}}
	if shouldLogSessionCardTiming(fast, time.Millisecond) {
		t.Fatal("fast successful card timing was logged")
	}
	if !shouldLogSessionCardTiming(fast, tracedSessionCardTiming) {
		t.Fatal("slow card timing was suppressed")
	}
	failed := fast
	failed.outcome = "transcript_error"
	if !shouldLogSessionCardTiming(failed, time.Millisecond) {
		t.Fatal("failed card timing was suppressed")
	}
	paneFailed := fast
	paneFailed.pane.outcome = "capture_error"
	if !shouldLogSessionCardTiming(paneFailed, time.Millisecond) {
		t.Fatal("pane failure timing was suppressed")
	}
}
