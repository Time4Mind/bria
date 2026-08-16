package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/hashicorp/raft"
)

func TestManualMembershipKeepsOnlyPreferredLeaderAsVoter(t *testing.T) {
	state := membershipTestState(domain.LeaderPolicy{NodeID: "leader"})
	state.Nodes["online"] = membershipTestNode("online", domain.NodeOnline)
	state.Nodes["offline"] = membershipTestNode("offline", domain.NodeOffline)
	configuration := membershipTestConfiguration(
		raft.Server{ID: "leader", Address: "leader:7946", Suffrage: raft.Voter},
		raft.Server{ID: "online", Address: "online:7946", Suffrage: raft.Voter},
		raft.Server{ID: "offline", Address: "offline:7946", Suffrage: raft.Voter},
	)

	action := nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipDemoteVoter, "offline")

	configuration.Servers[2].Suffrage = raft.Nonvoter
	action = nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipDemoteVoter, "online")

	configuration.Servers[1].Suffrage = raft.Nonvoter
	action = nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipNoop, "")
}

func TestManualMembershipPromotesThenTransfersToAvailablePreference(t *testing.T) {
	state := membershipTestState(domain.LeaderPolicy{NodeID: "target"})
	state.Nodes["target"] = membershipTestNode("target", domain.NodeOnline)
	configuration := membershipTestConfiguration(
		raft.Server{ID: "leader", Address: "leader:7946", Suffrage: raft.Voter},
		raft.Server{ID: "target", Address: "target:7946", Suffrage: raft.Nonvoter},
	)

	action := nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipEnsureVoter, "target")

	configuration.Servers[1].Suffrage = raft.Voter
	action = nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipTransferLeadership, "target")
}

func TestManualMembershipDoesNotPromoteUnavailablePreference(t *testing.T) {
	state := membershipTestState(domain.LeaderPolicy{NodeID: "target"})
	state.Nodes["target"] = membershipTestNode("target", domain.NodeOffline)
	configuration := membershipTestConfiguration(
		raft.Server{ID: "leader", Address: "leader:7946", Suffrage: raft.Voter},
		raft.Server{ID: "target", Address: "target:7946", Suffrage: raft.Nonvoter},
	)

	action := nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipNoop, "")
}

func TestManualMembershipUsesCurrentLeaderUntilPreferenceAssigned(t *testing.T) {
	state := membershipTestState(domain.LeaderPolicy{})
	state.Nodes["replica"] = membershipTestNode("replica", domain.NodeOnline)
	configuration := membershipTestConfiguration(
		raft.Server{ID: "leader", Address: "leader:7946", Suffrage: raft.Voter},
		raft.Server{ID: "replica", Address: "replica:7946", Suffrage: raft.Voter},
	)

	action := nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipDemoteVoter, "replica")
}

func TestManualMembershipAddsEnabledReplicaAsNonvoter(t *testing.T) {
	state := membershipTestState(domain.LeaderPolicy{NodeID: "leader"})
	state.Nodes["replica"] = membershipTestNode("replica", domain.NodeOnline)
	configuration := membershipTestConfiguration(
		raft.Server{ID: "leader", Address: "leader:7946", Suffrage: raft.Voter},
	)

	action := nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipEnsureNonvoter, "replica")
}

func TestAutomaticMembershipPromotesEveryEnabledNode(t *testing.T) {
	state := membershipTestState(domain.LeaderPolicy{Mode: domain.LeaderSelectionAutomatic})
	state.Nodes["replica"] = membershipTestNode("replica", domain.NodeOnline)
	configuration := membershipTestConfiguration(
		raft.Server{ID: "leader", Address: "leader:7946", Suffrage: raft.Voter},
		raft.Server{ID: "replica", Address: "replica:7946", Suffrage: raft.Nonvoter},
	)

	action := nextMembershipAction(configuration, state, "leader")
	assertMembershipAction(t, action, membershipEnsureVoter, "replica")
}

func membershipTestState(policy domain.LeaderPolicy) *domain.State {
	state := domain.NewState()
	state.LeaderPolicy = policy
	state.Nodes["leader"] = membershipTestNode("leader", domain.NodeOnline)
	return state
}

func membershipTestNode(nodeID domain.NodeID, status domain.NodeStatus) domain.Node {
	return domain.Node{
		ID: nodeID, Name: string(nodeID), Status: status, Lifecycle: domain.NodeActive,
		Network: domain.NodeNetwork{RaftAddress: string(nodeID) + ":7946"},
	}
}

func membershipTestConfiguration(servers ...raft.Server) raft.Configuration {
	return raft.Configuration{Servers: servers}
}

func assertMembershipAction(
	t *testing.T,
	action membershipAction,
	wantKind membershipActionKind,
	wantNodeID string,
) {
	t.Helper()
	if action.kind != wantKind || action.nodeID != wantNodeID {
		t.Fatalf("action=%+v, want kind=%d node=%q", action, wantKind, wantNodeID)
	}
}
