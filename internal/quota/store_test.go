package quota

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type fixedStateReader struct{ state *domain.State }

func (r fixedStateReader) State() *domain.State { return r.state }

type fixedCollector struct{ snapshot domain.QuotaSnapshot }

func (c fixedCollector) Backend() string { return c.snapshot.Backend }

func (c fixedCollector) Collect(context.Context, domain.NodeID) (domain.QuotaSnapshot, error) {
	c.snapshot.CollectedAt = time.Now()
	return c.snapshot, nil
}

func TestStoreRestoresDailyBaselineFromReplicatedQuota(t *testing.T) {
	now := time.Now()
	reset := now.Add(3 * 24 * time.Hour)
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "node", Name: "Node",
		Backends: []domain.BackendDescriptor{{Name: "codex"}},
	}); err != nil {
		t.Fatal(err)
	}
	previousRemaining := -3.0
	state.Quotas["node/codex"] = domain.QuotaSnapshot{
		NodeID: "node", Backend: "codex", CollectedAt: now.Add(-time.Minute),
		Weekly:         &domain.QuotaWindow{UsedPercent: 63, ResetsAt: reset},
		TodayRemaining: &previousRemaining,
		DailyBudget: &domain.QuotaDailyBudget{
			Date: now.Format("2006-01-02"), ResetsAt: reset,
			DayStartUsed: 50, Budget: 10,
		},
	}
	store := NewStore("node", fixedStateReader{state: state}, fixedCollector{
		snapshot: domain.QuotaSnapshot{
			NodeID: "node", Backend: "codex",
			Weekly: &domain.QuotaWindow{UsedPercent: 64, ResetsAt: reset},
		},
	})

	store.collect(context.Background())

	snapshots := store.Snapshots()
	if len(snapshots) != 1 || snapshots[0].TodayRemaining == nil ||
		snapshots[0].DailyBudget == nil || snapshots[0].DailyBudget.DayStartUsed != 50 ||
		*snapshots[0].TodayRemaining != snapshots[0].DailyBudget.Budget-14 {
		t.Fatalf("snapshots=%#v", snapshots)
	}
}
