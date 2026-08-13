package application_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestQuotaAlertsCollapseUniqueProviderAccount(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index, nodeID := range []domain.NodeID{"a", "b"} {
		snapshot := domain.QuotaSnapshot{
			NodeID: nodeID, Backend: "codex", AccountID: "account",
			AccountLabel: "owner@example.test", CollectedAt: now.Add(time.Duration(index) * time.Second),
			Weekly: &domain.QuotaWindow{UsedPercent: 40 + index},
		}
		if err := state.PublishNodeQuotas(nodeID, []domain.QuotaSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	observations := service.QuotaAlertObservations()
	if len(observations) != 1 || observations[0].UsedPercent != 41 ||
		observations[0].NodeID != "b" {
		t.Fatalf("observations=%#v", observations)
	}
}
