package clusterupdate

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestCoordinatorSkipsDormantNodeThatWillBootstrapOnReturn(t *testing.T) {
	state := domain.NewState()
	for index, node := range []domain.Node{
		{ID: "leader", Name: "leader", Status: domain.NodeOnline},
		{ID: "dormant", Name: "dormant", Status: domain.NodeOffline},
	} {
		node.Lifecycle = domain.NodeActive
		node.Version, node.OS, node.Arch = "v1", "linux", "amd64"
		node.CreatedAt = time.Unix(int64(index+1), 0)
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	machine := clusterstate.NewMachine(state)
	consensus := &coordinatorConsensus{machine: machine, leader: "leader", local: "leader"}
	nodes := &coordinatorNodes{offline: "dormant", manifest: VerifiedManifest{
		Manifest: Manifest{
			Version: "v2", MinimumNodeProtocol: 2,
			Artifacts: []Artifact{{OS: "linux", Arch: "amd64"}},
		},
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	coordinator, err := NewCoordinator("leader", machine, consensus, nodes)
	if err != nil {
		t.Fatal(err)
	}
	update, err := coordinator.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Order) != 1 || update.Order[0] != "leader" {
		t.Fatalf("update order=%v, want only online leader", update.Order)
	}
}
