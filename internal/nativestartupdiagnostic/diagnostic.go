// Package nativestartupdiagnostic classifies native adapter startup failures
// into bounded, allowlisted stages and classes safe for the parent process.
package nativestartupdiagnostic

import (
	"context"
	"errors"
	"strings"

	"bria/internal/nativetranscript"
	"bria/internal/runtimediagnostic"
)

type Stage string

const (
	StageUnknown               Stage = "unknown"
	StageOpenTerminal          Stage = "open_terminal"
	StageNativeReadinessStatus Stage = "native_readiness_status"
	StageBindingPersistence    Stage = "binding_persistence"
	StageReceiptBaseline       Stage = "receipt_baseline"
	StageProtocolReadyEmission Stage = "protocol_ready_emission"
)

type stageError struct {
	stage Stage
	cause error
}

func (e *stageError) Error() string { return e.cause.Error() }
func (e *stageError) Unwrap() error { return e.cause }

func AtStage(stage Stage, err error) error {
	if err == nil {
		return nil
	}
	return &stageError{stage: stage, cause: err}
}

func FailureStage(err error) Stage {
	var failure *stageError
	if errors.As(err, &failure) {
		return failure.stage
	}
	return StageUnknown
}

// FailureMarker is the exact bounded line consumed by the parent process.
func FailureMarker(err error) string {
	return strings.Join([]string{"bria-native-startup", string(FailureStage(err)), FailureClass(err)}, ":")
}

// FailureClass is the only error detail allowed onto adapter stderr.
func FailureClass(err error) string {
	if err == nil {
		return ""
	}
	var limit *nativetranscript.RecordLimitError
	if errors.As(err, &limit) {
		return "native_transcript_record_too_large"
	}
	for _, failure := range []struct {
		err   error
		class string
	}{
		{nativetranscript.ErrLimit, "native_transcript_read_limit"},
		{nativetranscript.ErrMalformed, "native_transcript_malformed"},
		{nativetranscript.ErrBinding, "native_transcript_binding_invalid"},
	} {
		if errors.Is(err, failure.err) {
			return failure.class
		}
	}
	var classified interface{ NativeStartupFailureClass() string }
	if errors.As(err, &classified) && classified.NativeStartupFailureClass() == "terminal_unavailable" {
		return "terminal_unavailable"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "readiness_timeout"
	}
	var diagnostic runtimediagnostic.Drain
	_, _ = diagnostic.Write([]byte(err.Error()))
	diagnostic.Finish()
	if class := runtimediagnostic.FailureClass(diagnostic.Wrap(err)); class != "" {
		return class
	}
	return "adapter_failed"
}
