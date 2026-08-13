package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/hashicorp/raft"
)

func TestResolverKeepsDisabledVoterUntilRaftRemovalCommits(t *testing.T) {
	resolver := consensus.NewStaticPeerResolver()
	state := domain.NewState()
	state.Nodes["beta"] = domain.Node{
		ID: "beta", Name: "Beta", Lifecycle: domain.NodeDisabled,
		Network: domain.NodeNetwork{RaftAddress: "beta:7946"},
	}
	known := make(map[domain.NodeID]string)
	syncMembershipResolver(resolver, state, known, map[string]string{"beta": "beta:7946"})
	if !resolver.IsApprovedNodeID("beta") {
		t.Fatal("disabled voter was revoked before its Raft removal")
	}
	if nodeID, ok := resolver.ExpectedNodeID(raft.ServerAddress("beta:7946")); !ok || nodeID != "beta" {
		t.Fatalf("configured voter address=%q/%v", nodeID, ok)
	}

	syncMembershipResolver(resolver, state, known, map[string]string{})
	if resolver.IsApprovedNodeID("beta") {
		t.Fatal("disabled node remained approved after its Raft removal")
	}
}

func TestResolverKeepsDeletedVoterUntilRaftRemovalCommits(t *testing.T) {
	resolver := consensus.NewStaticPeerResolver()
	state := domain.NewState()
	known := make(map[domain.NodeID]string)
	syncMembershipResolver(resolver, state, known, map[string]string{"beta": "beta:7946"})
	if !resolver.IsApprovedNodeID("beta") {
		t.Fatal("deleted voter was revoked before its Raft removal")
	}
	syncMembershipResolver(resolver, state, known, map[string]string{})
	if resolver.IsApprovedNodeID("beta") {
		t.Fatal("deleted node remained approved after its Raft removal")
	}
}
