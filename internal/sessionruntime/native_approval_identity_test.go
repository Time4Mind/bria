package sessionruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"bria/internal/domain"
)

type nativeApprovalBuffer struct{ bytes.Buffer }

func (nativeApprovalBuffer) Close() error { return nil }

func TestNativeControlRejectsStaleProviderBindingBeforeWriting(t *testing.T) {
	id := domain.SessionID("logical-session")
	child := &nativeApprovalBuffer{}
	starter := &Starter{processes: map[domain.SessionID]*processRecord{
		id: {
			stdin:   child,
			done:    make(chan struct{}),
			binding: domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "new-provider", Generation: 8},
		},
	}}
	_, err := starter.NativeControl(context.Background(), id, NativeRequest{
		Key: "approve_once", ExpectedHash: strings.Repeat("a", 64),
		ExpectedProviderSessionID: "old-provider", ExpectedGeneration: 7,
	})
	if !errors.Is(err, ErrNativeStale) {
		t.Fatalf("err=%v, want ErrNativeStale", err)
	}
	if child.Len() != 0 {
		t.Fatalf("stale approval wrote %d bytes to different child", child.Len())
	}
}

var _ io.WriteCloser = (*nativeApprovalBuffer)(nil)
