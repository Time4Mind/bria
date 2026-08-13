package application_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type projectionLeadership string

func (l projectionLeadership) LeaderID() string { return string(l) }

func TestStatusUsesGlobalNodeSortAndReplicatedQuota(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	now := time.Now().UTC()
	for nodeID, createdAt := range map[domain.NodeID]time.Time{
		"alpha": now.Add(-time.Minute), "beta": now.Add(-3 * time.Minute),
		"gamma": now.Add(-2 * time.Minute),
	} {
		node := state.Nodes[nodeID]
		node.CreatedAt = createdAt
		state.Nodes[nodeID] = node
	}
	preferences := state.Preferences[2]
	preferences.NodeSort = domain.NodeSortLeader
	state.Preferences[2] = preferences
	state.Quotas["gamma/codex"] = domain.QuotaSnapshot{
		NodeID: "gamma", Backend: "codex", CollectedAt: now,
		Weekly: &domain.QuotaWindow{UsedPercent: 41},
	}
	projector.SetLeadership(projectionLeadership("gamma"))
	screen, err := projector.Status(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	grid := telegramui.CanonicalGrid(screen.Grid)
	gammaAt, betaAt := strings.Index(grid, "Gamma"), strings.Index(grid, "Beta")
	alphaAt := strings.Index(grid, "Alpha")
	if gammaAt < 0 || !(gammaAt < betaAt && betaAt < alphaAt) {
		t.Fatalf("unexpected leader-first order:\n%s", grid)
	}
	if !strings.Contains(screen.Text, "👑 Gamma | codex | week 41%") {
		t.Fatalf("quota missing from status: %q", screen.Text)
	}
}

func TestClusterSettingsCollapseSameProviderAccount(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	now := time.Now().UTC()
	for _, nodeID := range []domain.NodeID{"alpha", "gamma"} {
		state.Quotas[string(nodeID)+"/codex"] = domain.QuotaSnapshot{
			NodeID: nodeID, Backend: "codex", AccountID: "account-1",
			AccountLabel: "owner@example.test", CollectedAt: now,
			Weekly: &domain.QuotaWindow{UsedPercent: 41},
		}
	}
	screen, err := projector.SettingsCategory(
		application.Principal{UserID: 2}, telegramui.CategoryCluster,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(screen.Text, "owner@example.test"); got != 1 {
		t.Fatalf("account occurrences=%d text=%q", got, screen.Text)
	}
}

func TestClusterSettingsCollapseManualProviderAlias(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	now := time.Now().UTC()
	for _, nodeID := range []domain.NodeID{"alpha", "gamma"} {
		node := state.Nodes[nodeID]
		node.Backends = []domain.BackendDescriptor{{Name: "codex"}}
		state.Nodes[nodeID] = node
		if err := state.SetProviderAccountAlias(nodeID, "codex", "Personal"); err != nil {
			t.Fatal(err)
		}
		state.Quotas[string(nodeID)+"/codex"] = domain.QuotaSnapshot{
			NodeID: nodeID, Backend: "codex", CollectedAt: now,
			Weekly: &domain.QuotaWindow{UsedPercent: 41},
		}
	}
	screen, err := projector.SettingsCategory(
		application.Principal{UserID: 2}, telegramui.CategoryCluster,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(screen.Text, "Personal"); got != 1 {
		t.Fatalf("manual alias occurrences=%d text=%q", got, screen.Text)
	}
}
