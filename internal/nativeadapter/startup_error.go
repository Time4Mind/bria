package nativeadapter

import (
	"context"
	"errors"

	"bria/internal/nativetranscript"
	"bria/internal/runtimediagnostic"
)

// StartupFailureClass is the only error detail allowed onto adapter stderr.
// Unknown errors never expose paths, provider output, credentials or prompts.
func StartupFailureClass(err error) string {
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
