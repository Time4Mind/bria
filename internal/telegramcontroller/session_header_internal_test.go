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
	header := telegramsessionview.Header("default", "Local", domain.ProviderCodex, event.UnixNano(), "gpt-5.6-sol")
	want := "default · Local · codex · gpt-5.6-sol · 17:10:37\n\n─────  \n"
	if header != want {
		t.Fatalf("header = %q, want %q", header, want)
	}
	if strings.Contains(header, "готова") || strings.Contains(header, "не готова") {
		t.Fatalf("header contains redundant readiness copy: %q", header)
	}
	if got := telegramsessionview.Header("default", "Local", domain.ProviderCodex, 0, ""); strings.Contains(got, " · 00:00:00") {
		t.Fatalf("empty activity rendered zero timestamp: %q", got)
	}
}
