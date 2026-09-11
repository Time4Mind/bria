package turnfailure

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"bria/internal/sessionruntime"
)

func TestIntentionalCancellationSuppressesOnlyPureCancellation(t *testing.T) {
	providerErr := errors.New("provider failed")
	for _, test := range []struct {
		name   string
		result sessionruntime.TurnResult
		err    error
		want   bool
	}{
		{name: "plain cancellation", err: context.Canceled},
		{name: "wrapped cancellation", err: fmt.Errorf("shutdown: %w", context.Canceled)},
		{name: "joined cancellations", err: errors.Join(context.Canceled, fmt.Errorf("wrapped: %w", context.Canceled))},
		{name: "joined provider marker", err: errors.Join(context.Canceled, sessionruntime.ErrTurnFailed), want: true},
		{name: "joined arbitrary provider error", err: errors.Join(context.Canceled, providerErr), want: true},
		{name: "failed terminal wins", result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed}, err: context.Canceled, want: true},
		{name: "interrupted terminal", result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted}, err: providerErr},
		{name: "interrupted code", result: sessionruntime.TurnResult{ErrorCode: sessionruntime.ErrorInterrupted}, err: providerErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsProviderFailure(test.result, test.err, true); got != test.want {
				t.Fatalf("IsProviderFailure()=%t, want %t", got, test.want)
			}
		})
	}
}
