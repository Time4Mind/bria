package telegrambridge

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"bria/internal/telegram"
)

func TestNextRetryDelayUsesBoundedProductSequence(t *testing.T) {
	t.Parallel()

	delay := 4 * time.Second
	want := []time.Duration{8 * time.Second, 12 * time.Second, 15 * time.Second, 15 * time.Second}
	for index, expected := range want {
		delay = nextRetryDelay(delay)
		if delay != expected {
			t.Fatalf("step %d delay = %v, want %v", index+1, delay, expected)
		}
	}
}

func TestRetryDelayForHonorsLongerStructuredRetryAfter(t *testing.T) {
	t.Parallel()

	err := &telegram.APIError{HTTPStatus: http.StatusTooManyRequests, ErrorCode: http.StatusTooManyRequests, RetryAfter: 9 * time.Second}
	if got := retryDelayFor(err, 4*time.Second); got != 9*time.Second {
		t.Fatalf("retry delay = %v, want 9s", got)
	}
	if got := retryDelayFor(errors.New("temporary"), 4*time.Second); got != 4*time.Second {
		t.Fatalf("fallback delay = %v, want 4s", got)
	}
}
