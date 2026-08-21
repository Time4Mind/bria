package main

import (
	"testing"
	"time"

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

func TestTranscriptRuntimeHeartbeatWaitsForMatchingLeaderVersion(t *testing.T) {
	state := domain.NewState()
	state.Nodes["leader"] = domain.Node{ID: "leader", Name: "Leader", Version: "new"}
	if !transcriptRuntimeHeartbeatEnabled(state, "leader", "new") {
		t.Fatal("matching leader version did not enable transcript runtime")
	}
	if transcriptRuntimeHeartbeatEnabled(state, "leader", "old") {
		t.Fatal("mismatched leader version enabled a new heartbeat field")
	}
}

func TestRestoreHeartbeatWaitOnlyMeasuresArchiveRestore(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 5, 0, time.UTC)
	archiveRestore := domain.Session{
		ArchiveID: "archive", ArchiveReason: "closed",
		LiveSinceAt: startedAt.Add(-5 * time.Second),
	}
	if got := restoreHeartbeatWait(archiveRestore, startedAt); got != 5*time.Second {
		t.Fatalf("archive restore heartbeat wait=%s", got)
	}
	bootRecovery := archiveRestore
	bootRecovery.ArchiveID = ""
	bootRecovery.ArchiveReason = ""
	bootRecovery.LiveSinceAt = startedAt.Add(-24 * time.Hour)
	if got := restoreHeartbeatWait(bootRecovery, startedAt); got != 0 {
		t.Fatalf("boot recovery reported stale live age as heartbeat wait=%s", got)
	}
}
