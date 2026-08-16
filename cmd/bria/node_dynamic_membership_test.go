package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/hashicorp/raft"
)

func TestResolverKeepsSeedPeersUntilInitialStateArrives(t *testing.T) {
	resolver := consensus.NewStaticPeerResolver()
	resolver.Set("android.bria.internal:17946", "android")
	resolver.ApproveNodeID("android")
	known := map[domain.NodeID]string{"android": "android.bria.internal:17946"}

	if syncMembershipResolverIfReady(resolver, domain.NewState(), known, map[string]string{}) {
		t.Fatal("empty joining state was treated as authoritative")
	}

	if !resolver.IsApprovedNodeID("android") {
		t.Fatal("seed peer was revoked before the initial Raft snapshot")
	}
	if nodeID, ok := resolver.ExpectedNodeID("android.bria.internal:17946"); !ok || nodeID != "android" {
		t.Fatalf("seed peer address=%q/%v", nodeID, ok)
	}
}

func TestResolverKeepsSeedPeersWithSelfOnlyRaftConfiguration(t *testing.T) {
	resolver := consensus.NewStaticPeerResolver()
	resolver.Set("android.bria.internal:17946", "android")
	resolver.ApproveNodeID("android")
	resolver.Set("new-node.internal:47946", "new-node")
	resolver.ApproveNodeID("new-node")
	known := map[domain.NodeID]string{
		"android":  "android.bria.internal:17946",
		"new-node": "new-node.internal:47946",
	}

	configured := map[string]string{"new-node": "new-node.internal:47946"}
	if syncMembershipResolverIfReady(resolver, domain.NewState(), known, configured) {
		t.Fatal("self-only Raft configuration was treated as replicated state")
	}
	if !resolver.IsApprovedNodeID("android") {
		t.Fatal("seed peer was revoked before the initial snapshot")
	}
}

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

func TestDynamicMembershipRelocationPreservesLocalDialOverride(t *testing.T) {
	resolver := consensus.NewStaticPeerResolver()
	resolver.SetDialAddress("beta", "127.0.0.1:19046")
	state := domain.NewState()
	state.Nodes["beta"] = domain.Node{
		ID: "beta", Lifecycle: domain.NodeActive,
		Network: domain.NodeNetwork{RaftAddress: "old.bria.internal:7946"},
	}
	known := make(map[domain.NodeID]string)
	syncMembershipResolver(resolver, state, known, map[string]string{})
	if address, ok := resolver.DialAddress(raft.ServerAddress("old.bria.internal:7946")); !ok || address != "127.0.0.1:19046" {
		t.Fatalf("initial override=%q/%v", address, ok)
	}

	relocated := state.Nodes["beta"]
	relocated.Network.RaftAddress = "new.bria.internal:17946"
	state.Nodes["beta"] = relocated
	syncMembershipResolver(resolver, state, known, map[string]string{})
	if address, ok := resolver.DialAddress(raft.ServerAddress("new.bria.internal:17946")); !ok || address != "127.0.0.1:19046" {
		t.Fatalf("relocated override=%q/%v", address, ok)
	}
	if _, ok := resolver.ExpectedNodeID(raft.ServerAddress("old.bria.internal:7946")); ok {
		t.Fatal("obsolete advertised address remained authenticated after relocation")
	}
}

func TestDynamicMembershipRemovesStaleBootstrapAddressAfterRestart(t *testing.T) {
	resolver := consensus.NewStaticPeerResolver()
	resolver.Set("bootstrap.bria.internal:7946", "beta")
	resolver.SetDialAddress("beta", "127.0.0.1:19046")
	state := domain.NewState()
	state.Nodes["beta"] = domain.Node{
		ID: "beta", Lifecycle: domain.NodeActive,
		Network: domain.NodeNetwork{RaftAddress: "relocated.bria.internal:17946"},
	}
	known := map[domain.NodeID]string{"beta": "bootstrap.bria.internal:7946"}
	syncMembershipResolver(
		resolver,
		state,
		known,
		map[string]string{"beta": "relocated.bria.internal:17946"},
	)
	if _, ok := resolver.ExpectedNodeID("bootstrap.bria.internal:7946"); ok {
		t.Fatal("stale bootstrap address remained after restart")
	}
	if address, ok := resolver.DialAddress("relocated.bria.internal:17946"); !ok || address != "127.0.0.1:19046" {
		t.Fatalf("relocated override=%q/%v", address, ok)
	}
}
