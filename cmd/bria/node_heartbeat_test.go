package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

type heartbeatTestLeader string

func (l heartbeatTestLeader) LeaderID() string { return string(l) }

type heartbeatTestState struct {
	state *domain.State
}

func (s heartbeatTestState) State() *domain.State { return s.state.Clone() }

func TestHeartbeatLeaderResolverPrefersRaftEvidence(t *testing.T) {
	state := domain.NewState()
	state.Nodes["preferred"] = domain.Node{
		ID: "preferred", Name: "Preferred", Lifecycle: domain.NodeActive,
	}
	state.LeaderPolicy = domain.LeaderPolicy{NodeID: "preferred"}
	resolver := heartbeatLeaderResolver{
		raft: heartbeatTestLeader("current"), state: heartbeatTestState{state},
	}
	if got := resolver.LeaderID(); got != "current" {
		t.Fatalf("leader=%q, want current Raft leader", got)
	}
}

func TestHeartbeatLeaderResolverFallsBackToManualPreference(t *testing.T) {
	state := domain.NewState()
	state.Nodes["preferred"] = domain.Node{
		ID: "preferred", Name: "Preferred", Lifecycle: domain.NodeActive,
	}
	state.LeaderPolicy = domain.LeaderPolicy{NodeID: "preferred"}
	resolver := heartbeatLeaderResolver{
		raft: heartbeatTestLeader(""), state: heartbeatTestState{state},
	}
	if got := resolver.LeaderID(); got != "preferred" {
		t.Fatalf("leader=%q, want manual preference", got)
	}
}

func TestHeartbeatLeaderResolverHasNoAutomaticFallback(t *testing.T) {
	state := domain.NewState()
	state.Nodes["preferred"] = domain.Node{
		ID: "preferred", Name: "Preferred", Lifecycle: domain.NodeActive,
	}
	state.LeaderPolicy = domain.LeaderPolicy{
		Mode: domain.LeaderSelectionAutomatic, NodeID: "preferred",
	}
	resolver := heartbeatLeaderResolver{
		raft: heartbeatTestLeader(""), state: heartbeatTestState{state},
	}
	if got := resolver.LeaderID(); got != "" {
		t.Fatalf("automatic fallback leader=%q", got)
	}
}
