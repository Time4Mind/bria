package sessionruntime_test

import (
	"context"
	"strings"
	"testing"

	"bria/internal/sessionruntime"
)

func TestTurnExitPreservesSafeNativeFailureClass(t *testing.T) {
	starter, request, binding := startHelper(t, "runtime-diagnostic", sessionruntime.Options{})
	defer starter.Abort(context.Background(), request, binding)
	accepted := false
	_, err := starter.SubmitWithCallbacks(context.Background(), request.SessionID, "synthetic", sessionruntime.TurnCallbacks{
		MessageID: "diagnostic-message", OnAccepted: func(string) error { accepted = true; return nil },
	})
	if !accepted || err == nil || sessionruntime.StartupFailureClass(err) != "native_transcript_record_too_large" {
		t.Fatalf("accepted=%v error=%v class=%s", accepted, err, sessionruntime.StartupFailureClass(err))
	}
	if strings.Contains(err.Error(), "private-payload") {
		t.Fatal("private stderr escaped")
	}
}
