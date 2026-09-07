package telegramcontroller

import (
	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"testing"
	"time"
)

func TestStandbyTargetScopesPopularityAndCountsConsumedStandby(t *testing.T) {
	makeSession := func(id, node, dir string, provider domain.Provider) domain.Session {
		s, err := domain.NewStartingSessionAt(domain.SessionID(id), domain.IntentID("standby:"+id), domain.ComputerID(node), provider, dir, time.Unix(100, 0), domain.SessionLifetimeNever)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	sessions := []domain.Session{makeSession("a", "local", "/popular", domain.ProviderClaude), makeSession("b", "local", "/popular", domain.ProviderClaude), makeSession("c", "local", "/other", domain.ProviderCodex)}
	for i := 0; i < 10; i++ {
		sessions = append(sessions, makeSession("remote", "remote", "/remote", domain.ProviderCodex))
	}
	caps := []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}, {Provider: domain.ProviderClaude, Installed: true, Enabled: true}}
	target := standbyTarget("local", sessions, caps, domain.ProviderCodex)
	if target.Workdir != "/popular" || target.Provider != domain.ProviderCodex || target.ComputerID != "local" {
		t.Fatalf("target=%#v", target)
	}
	caps[0].Enabled = false
	target = standbyTarget("local", sessions, caps, domain.ProviderCodex)
	if target.Provider != domain.ProviderClaude {
		t.Fatalf("disabled default chosen: %#v", target)
	}
}
