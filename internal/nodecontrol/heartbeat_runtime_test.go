package nodecontrol

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestHeartbeatRuntimeReportRepairsRecoveredOpenTurnWithExistingCommand(t *testing.T) {
	state := domain.NewState()
	created := time.Unix(10, 0).UTC()
	if err := state.AddNode(domain.Node{
		ID: "node", Name: "Node", Status: domain.NodeOnline, CreatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Session", Backend: "codex",
		ProviderSessionID: "provider", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 3,
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	committer, err := NewConsensusHeartbeatCommitter(machineApplier{machine}, machine)
	if err != nil {
		t.Fatal(err)
	}
	committer.now = func() time.Time { return time.Unix(30, 0).UTC() }
	err = committer.commitTranscriptRuntime(context.Background(), Heartbeat{
		NodeID: "node",
		Runtime: []domain.TranscriptRuntimeReport{{
			SessionID: "session", Generation: 3, Phase: domain.RuntimeRunning,
			Timestamp: time.Unix(20, 0).UTC(),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := machine.State().Sessions["node/session"]
	if session.RuntimePhase != domain.RuntimeRunning ||
		session.LastEventAt != time.Unix(20, 0).UTC() {
		t.Fatalf("reconciled session=%#v", session)
	}
}
