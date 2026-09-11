package telegramcontroller

import (
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramsessionview"
)

func TestSessionCardHeaderShowsLocalLastEventTime(t *testing.T) {
	event := time.Date(2026, 9, 11, 14, 10, 37, 0, time.UTC)
	header := telegramsessionview.Header("default", "Local", domain.ProviderCodex, "готова", event.UnixNano())
	want := "17:10:37"
	if !strings.Contains(header, " · "+want+"\n") {
		t.Fatalf("header = %q, want event time %q", header, want)
	}
	if got := telegramsessionview.Header("default", "Local", domain.ProviderCodex, "готова", 0); strings.Contains(got, " · 00:00:00") {
		t.Fatalf("empty activity rendered zero timestamp: %q", got)
	}
}
