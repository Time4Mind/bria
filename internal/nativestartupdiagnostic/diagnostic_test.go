package nativestartupdiagnostic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bria/internal/nativetranscript"
)

func TestFailureClassNeverEchoesPrivateError(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{errors.New("private-token=/secret/path"), "adapter_failed"},
		{errors.New("prefix native CLI authentication required private-token"), "adapter_failed"},
		{errors.New("native CLI authentication required"), "authentication_required"},
		{errors.Join(errors.New("native CLI workspace trust confirmation required"), errors.New("private-path")), "workspace_trust_required"},
		{context.DeadlineExceeded, "readiness_timeout"},
		{nativetranscript.ErrLimit, "native_transcript_read_limit"},
		{&nativetranscript.RecordLimitError{Offset: 1, Observed: 3, Limit: 2}, "native_transcript_record_too_large"},
		{nativetranscript.ErrMalformed, "native_transcript_malformed"},
	} {
		if got := FailureClass(test.err); got != test.want {
			t.Fatalf("FailureClass(%T)=%q want %q", test.err, got, test.want)
		}
	}
}

func TestFailureMarkerIsBoundedAndAllowlisted(t *testing.T) {
	private := "token=private /secret/path prompt-text"
	for _, stage := range []Stage{
		StageUnknown,
		StageOpenTerminal,
		StageNativeReadinessStatus,
		StageBindingPersistence,
		StageReceiptBaseline,
		StageProtocolReadyEmission,
	} {
		err := error(errors.New(private))
		if stage != StageUnknown {
			err = AtStage(stage, err)
		}
		want := "bria-native-startup:" + string(stage) + ":adapter_failed"
		if got := FailureMarker(err); got != want || strings.Contains(got, private) || len(got) > 128 {
			t.Fatalf("unsafe startup marker %q want %q", got, want)
		}
	}
}

func TestAtStagePreservesCauseAndNil(t *testing.T) {
	cause := errors.New("cause")
	wrapped := AtStage(StageOpenTerminal, cause)
	if !errors.Is(wrapped, cause) || FailureStage(wrapped) != StageOpenTerminal {
		t.Fatalf("wrapped stage=%q error=%v", FailureStage(wrapped), wrapped)
	}
	if AtStage(StageOpenTerminal, nil) != nil {
		t.Fatal("nil error was wrapped")
	}
}
