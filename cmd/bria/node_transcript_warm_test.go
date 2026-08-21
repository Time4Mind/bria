package main

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestLocalTranscriptWarmRequestsPrioritizeVisibleCard(t *testing.T) {
	state := domain.NewState()
	visible := domain.Session{
		ID: "visible", NodeID: "mac", OwnerID: 7, Backend: "codex",
		ProviderSessionID: "visible-provider", Workdir: "/visible",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeRunning,
	}
	recent := domain.Session{
		ID: "recent", NodeID: "mac", OwnerID: 7, Backend: "codex",
		ProviderSessionID: "recent-provider", Workdir: "/recent",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeRunning,
		LastEventAt: time.Now(),
	}
	state.Sessions[visible.Ref().Key()] = visible
	state.Sessions[recent.Ref().Key()] = recent
	state.TelegramResponseCards[7] = domain.TelegramResponseCard{Session: visible.Ref()}

	requests := localTranscriptWarmRequests(state, "mac")
	if len(requests) != 2 || requests[0].ProviderSessionID != "visible-provider" ||
		requests[1].ProviderSessionID != "recent-provider" {
		t.Fatalf("warm requests=%#v", requests)
	}
}
