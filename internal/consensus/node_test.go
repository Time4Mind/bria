package consensus_test

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestSingleNodeConsensusAppliesAndSnapshotsState(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	node, err := consensus.NewInMemory(
		consensus.Config{NodeID: "alpha", ApplyTimeout: 2 * time.Second},
		clusterstate.NewFSM(machine),
	)
	if err != nil {
		t.Fatalf("new in-memory node: %v", err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Errorf("close node: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := node.WaitForLeader(ctx); err != nil {
		t.Fatalf("wait for leader: %v", err)
	}
	if got := node.LeaderID(); got != "alpha" {
		t.Fatalf("leader ID = %q, want alpha", got)
	}
	command, err := clusterstate.NewCommand(
		"add-alpha",
		clusterstate.CommandAddNode,
		time.Unix(100, 0),
		domain.Node{ID: "alpha", Name: "Alpha"},
	)
	if err != nil {
		t.Fatalf("new command: %v", err)
	}
	result, err := node.Apply(ctx, command)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := result.Err(); err != nil {
		t.Fatalf("state machine result: %v", err)
	}
	if got := node.State().State().Nodes["alpha"].Name; got != "Alpha" {
		t.Fatalf("node name = %q", got)
	}
}
