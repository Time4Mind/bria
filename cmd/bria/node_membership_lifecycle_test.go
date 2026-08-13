package main

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func TestDisabledNodeLosesControlBeforeVoterRemovalAndCannotRejoin(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	node, err := consensus.NewInMemory(
		consensus.Config{NodeID: "alpha", ApplyTimeout: time.Second},
		clusterstate.NewFSM(machine),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := node.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	for _, member := range []domain.Node{
		{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline, Lifecycle: domain.NodeActive},
		{ID: "beta", Name: "Beta", Status: domain.NodeOnline, Lifecycle: domain.NodeActive,
			Fingerprint: "fingerprint", Network: domain.NodeNetwork{RaftAddress: "beta:7946"}},
	} {
		applyMembershipCommand(t, ctx, node, "add-"+member.Name, clusterstate.CommandAddNode, member)
	}
	resolver := consensus.NewStaticPeerResolver()
	known := make(map[domain.NodeID]string)
	configured := map[string]string{"alpha": "alpha", "beta": "beta:7946"}
	syncMembershipResolver(resolver, node.State().State(), known, configured)
	if !resolver.IsApprovedNodeID("beta") {
		t.Fatal("active voter was not approved")
	}
	applyMembershipCommand(t, ctx, node, "disable-beta", clusterstate.CommandSetNodeLifecycle,
		clusterstate.SetNodeLifecycle{NodeID: "beta", Lifecycle: domain.NodeDisabled})
	guard, err := nodecontrol.NewStateGuard(node.State())
	if err != nil {
		t.Fatal(err)
	}
	if guard.IsMember("beta") {
		t.Fatal("disabled voter retained node-control membership")
	}
	syncMembershipResolver(resolver, node.State().State(), known, configured)
	if !resolver.IsApprovedNodeID("beta") {
		t.Fatal("disabled voter lost Raft admission before removal committed")
	}
	syncMembershipResolver(resolver, node.State().State(), known, map[string]string{"alpha": "alpha"})
	if resolver.IsApprovedNodeID("beta") {
		t.Fatal("removed voter retained Raft admission")
	}
	applyMembershipCommand(t, ctx, node, "delete-beta", clusterstate.CommandDeleteNode,
		clusterstate.DeleteNode{NodeID: "beta"})
	result := applyMembershipCommandResult(
		t, ctx, node, "rejoin-beta", clusterstate.CommandAddNode,
		domain.Node{ID: "beta", Name: "Replacement", Fingerprint: "other"},
	)
	if result.Err() == nil {
		t.Fatal("deleted node identity rejoined despite tombstone")
	}
}

func applyMembershipCommand(
	t *testing.T,
	ctx context.Context,
	node *consensus.Node,
	operationID string,
	kind clusterstate.CommandKind,
	payload any,
) {
	t.Helper()
	if err := applyMembershipCommandResult(t, ctx, node, operationID, kind, payload).Err(); err != nil {
		t.Fatalf("apply %s: %v", operationID, err)
	}
}

func applyMembershipCommandResult(
	t *testing.T,
	ctx context.Context,
	node *consensus.Node,
	operationID string,
	kind clusterstate.CommandKind,
	payload any,
) clusterstate.Result {
	t.Helper()
	command, err := clusterstate.NewCommand(operationID, kind, time.Now(), payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := node.Apply(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
