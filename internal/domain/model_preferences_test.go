package domain_test

import (
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
)

func TestModelPreferencesAreValidatedAndImmutable(t *testing.T) {
	starting, err := domain.NewStartingSession("session", "intent", "node", domain.ProviderCodex, "/work")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{" leading", "trailing ", "two words", "line\nbreak", "nul\x00", "a\u200bb", string([]byte{0xff}), strings.Repeat("x", 129)} {
		for _, model := range []bool{true, false} {
			m, e := "gpt-5", value
			if model {
				m, e = value, "high"
			}
			if _, err := ready.WithModelPreferences(m, e); err == nil {
				t.Fatalf("accepted unsafe preference %q", value)
			}
			snapshot := ready.Snapshot()
			snapshot.Model, snapshot.Effort = m, e
			if _, err := domain.RestoreSession(snapshot); err == nil {
				t.Fatalf("restored unsafe preference %q", value)
			}
		}
	}
	next, err := ready.WithModelPreferences("provider/gpt-5.2", "high")
	if err != nil {
		t.Fatal(err)
	}
	if ready.Snapshot().Model != "" || ready.Equal(next) {
		t.Fatal("mutation or equality lost model change")
	}
	effortOnly, err := next.WithModelPreferences("provider/gpt-5.2", "low")
	if err != nil || effortOnly.Equal(next) {
		t.Fatal("equality lost effort change")
	}
	running, err := next.StartWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if running.Snapshot().Model != "provider/gpt-5.2" || running.Snapshot().Effort != "high" {
		t.Fatal("transition lost preferences")
	}
	if _, err := running.WithModelPreferences("", ""); err == nil {
		t.Fatal("running mutation accepted")
	}
	reset, err := next.WithModelPreferences("", "")
	if err != nil || !ready.Equal(reset) {
		t.Fatal("default reset changed lifecycle")
	}
}
