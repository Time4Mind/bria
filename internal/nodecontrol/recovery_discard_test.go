package nodecontrol

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestRemoteRecoveryApplierReportsCompletedDiscard(t *testing.T) {
	reporter := &recoveryReporterRecorder{}
	applier, err := NewRemoteRecoveryApplier(
		"target", &mutableLeadership{id: "leader"}, reporter,
	)
	if err != nil {
		t.Fatal(err)
	}
	discard := recoveryCommand(t, clusterstate.CommandCompleteSessionDiscard, domain.SessionRef{
		NodeID: "target", SessionID: "session",
	})
	discard.Payload, _ = json.Marshal(clusterstate.SessionRevision{
		ActorID: 7, Session: domain.SessionRef{NodeID: "target", SessionID: "session"},
		ExpectedRevision: 3,
	})
	if _, err := applier.Apply(context.Background(), discard); err != nil {
		t.Fatal(err)
	}
	if reporter.report.Outcome != RecoveryDiscarded || reporter.report.ActorID != 7 ||
		reporter.report.ExpectedRevision != 3 {
		t.Fatalf("discard recovery report=%#v", reporter.report)
	}
}
