package nativeadapter

import (
	"bria/internal/nativetranscript"
	"context"
	"errors"
	"testing"
)

func TestStartupClassNeverEchoesPrivateError(t *testing.T) {
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
		if got := StartupFailureClass(test.err); got != test.want {
			t.Fatal("unsafe or wrong startup class")
		}
	}
}
