package nodecontrol

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestRemoteRecoveryApplierReportsRuntimeReattach(t *testing.T) {
	reporter := &recoveryReporterRecorder{}
	applier, err := NewRemoteRecoveryApplier(
		"target", &mutableLeadership{id: "leader"}, reporter,
	)
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "target", SessionID: "session"}
	command := recoveryCommand(t, clusterstate.CommandReattachSessionRuntime, ref)
	command.Payload, _ = json.Marshal(clusterstate.ReattachSessionRuntime{
		Session: ref, ExpectedGeneration: 4, ExpectedRevision: 3,
	})
	if _, err := applier.Apply(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if reporter.report.Outcome != RecoveryReattached ||
		reporter.report.ExpectedGeneration != 4 || reporter.report.ExpectedRevision != 3 {
		t.Fatalf("reattach recovery report=%#v", reporter.report)
	}
}

func TestRecoveryCommitterReattachesRuntimeWithVersionGates(t *testing.T) {
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
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Session", Workdir: "/work",
		Backend: "codex", State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle,
		RuntimeGeneration: 4, CreatedAt: created, LiveSinceAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	session := state.Sessions[ref.Key()]
	if err := state.ArchiveSession(
		ref, session.Revision, domain.ArchiveResumeFailed, time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	archived := state.Sessions[ref.Key()]
	machine := clusterstate.NewMachine(state)
	committer, err := NewConsensusHeartbeatCommitter(machineApplier{machine}, machine)
	if err != nil {
		t.Fatal(err)
	}
	committer.now = func() time.Time { return time.Unix(30, 0).UTC() }
	if err := committer.CommitRecovery(context.Background(), RecoveryReport{
		ReportID: "reattach-result", NodeID: "node", Session: ref,
		Outcome: RecoveryReattached, ExpectedGeneration: archived.RuntimeGeneration,
		ExpectedRevision: archived.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if got := machine.State().Sessions[ref.Key()]; !got.IsLive() ||
		got.RuntimePhase != domain.RuntimeIdle || got.Revision != archived.Revision+1 {
		t.Fatalf("reattached session=%#v", got)
	}
}
