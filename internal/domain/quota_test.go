package domain_test

import (
	"math"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestQuotaSnapshotValidationAndCloneIsolation(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node"}); err != nil {
		t.Fatal(err)
	}
	remaining := 81.5
	snapshot := domain.QuotaSnapshot{
		NodeID: "node", Backend: "codex", AccountID: "acct",
		FiveHour: &domain.QuotaWindow{UsedPercent: 25}, TodayRemaining: &remaining,
		CollectedAt: time.Unix(100, 0),
	}
	if err := state.PublishNodeQuotas("node", []domain.QuotaSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	snapshot.FiveHour.UsedPercent = 99
	remaining = 0
	got := state.Quotas["node/codex"]
	if got.FiveHour.UsedPercent != 25 || *got.TodayRemaining != 81.5 {
		t.Fatalf("quota aliased caller: %#v", got)
	}
	invalid := got
	nan := math.NaN()
	invalid.TodayRemaining = &nan
	if err := invalid.Validate(); err == nil {
		t.Fatal("NaN daily remainder accepted")
	}
	older := got
	older.CollectedAt = got.CollectedAt.Add(-time.Second)
	older.FiveHour = &domain.QuotaWindow{UsedPercent: 1}
	if err := state.PublishNodeQuotas("node", []domain.QuotaSnapshot{older}); err != nil {
		t.Fatal(err)
	}
	if state.Quotas["node/codex"].FiveHour.UsedPercent != 25 {
		t.Fatal("older quota snapshot replaced current data")
	}
}

func TestNewNodeIsAutomaticallyGrantedToOwner(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "first", Name: "First"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "first"); err != nil {
		t.Fatal(err)
	}
	if err := state.AddNode(domain.Node{ID: "second", Name: "Second"}); err != nil {
		t.Fatal(err)
	}
	if !state.CanAccessNode(7, "second") {
		t.Fatal("owner did not receive new node")
	}
}

func TestTemporaryLeaderRejectsOfflineAndLongPins(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if err := state.SetTemporaryLeader("node", now.Add(30*time.Minute), now); err == nil {
		t.Fatal("offline node accepted")
	}
	node := state.Nodes["node"]
	node.Status = domain.NodeOnline
	state.Nodes["node"] = node
	if err := state.SetTemporaryLeader("node", now.Add(61*time.Minute), now); err == nil {
		t.Fatal("overlong leader pin accepted")
	}
	if err := state.SetTemporaryLeader("node", now.Add(30*time.Minute), now); err != nil {
		t.Fatal(err)
	}
}

func TestLeaderSelectionDefaultsManualAndPersistsAssignment(t *testing.T) {
	state := domain.NewState()
	if state.LeaderPolicy.EffectiveMode() != domain.LeaderSelectionManual {
		t.Fatal("new state did not default to manual leader selection")
	}
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetPreferredLeader("node"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetLeaderSelectionMode(domain.LeaderSelectionAutomatic); err != nil {
		t.Fatal(err)
	}
	clone := state.Clone()
	if clone.LeaderPolicy.NodeID != "node" ||
		clone.LeaderPolicy.EffectiveMode() != domain.LeaderSelectionAutomatic {
		t.Fatalf("leader policy=%+v", clone.LeaderPolicy)
	}
}
