package clusterstate_test

import (
	"testing"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestPermanentLeaderPolicyIsReplicated(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	assign := clusterstate.SetPreferredLeader{NodeID: "alpha"}
	if result := machine.Apply(command(t, "assign-leader", clusterstate.CommandSetPreferredLeader, assign)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	mode := clusterstate.SetLeaderSelectionMode{Mode: domain.LeaderSelectionAutomatic}
	if result := machine.Apply(command(t, "auto-leader", clusterstate.CommandSetLeaderSelectionMode, mode)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	got := machine.State().LeaderPolicy
	if got.NodeID != "alpha" || got.EffectiveMode() != domain.LeaderSelectionAutomatic {
		t.Fatalf("leader policy=%+v", got)
	}
}
