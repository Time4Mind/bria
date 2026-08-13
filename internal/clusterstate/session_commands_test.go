package clusterstate_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestSessionRuntimeCommandsAreDeterministicAndGenerationGuarded(t *testing.T) {
	machine, ref := sessionMachine(t)
	session := machine.State().Sessions[ref.Key()]
	publish := command(t, "runtime-running", clusterstate.CommandPublishSessionRuntime,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration, Phase: domain.RuntimeRunning,
			Result: &domain.SessionOperationResult{
				OperationID: "telegram-42",
				Action:      domain.ActionSendInput,
				Status:      domain.OperationSucceeded,
			},
		})
	if result := machine.Apply(publish); result.Err() != nil {
		t.Fatal(result.Err())
	}
	if result := machine.Apply(publish); result.Err() != nil {
		t.Fatalf("deduplicated publication=%v", result.Err())
	}
	updated := machine.State().Sessions[ref.Key()]
	if updated.RuntimePhase != domain.RuntimeRunning || updated.Revision != session.Revision+1 {
		t.Fatalf("published session=%#v", updated)
	}

	clear := command(t, "clear-1", clusterstate.CommandClearSession, clusterstate.SessionRevision{
		ActorID: 1, Session: ref, ExpectedRevision: updated.Revision,
	})
	if result := machine.Apply(clear); result.Err() != nil {
		t.Fatal(result.Err())
	}
	cleared := machine.State().Sessions[ref.Key()]
	if cleared.Name != "" || cleared.ProviderSessionID != "" ||
		cleared.RuntimeGeneration != session.RuntimeGeneration+1 {
		t.Fatalf("cleared session=%#v", cleared)
	}

	stale := command(t, "late-result", clusterstate.CommandPublishSessionRuntime,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration, Phase: domain.RuntimeIdle,
		})
	if result := machine.Apply(stale); result.Error != domain.ErrStaleOperation.Error() {
		t.Fatalf("late result error=%v", result.Err())
	}
}

func TestCloseAndAutomaticArchiveCommandsUseDifferentAuthority(t *testing.T) {
	machine, ref := sessionMachine(t)
	revision := machine.State().Sessions[ref.Key()].Revision
	manualArchive := command(t, "bad-auto-manual", clusterstate.CommandArchiveSession,
		clusterstate.ArchiveSession{
			Session: ref, ExpectedRevision: revision, Reason: domain.ArchiveManual,
		})
	if result := machine.Apply(manualArchive); result.Err() == nil {
		t.Fatal("automatic command accepted manual close reason")
	}
	closeCommand := command(t, "close-1", clusterstate.CommandCloseSession,
		clusterstate.SessionRevision{
			ActorID: 1, Session: ref, ExpectedRevision: revision,
			ArchiveCommitID: "archive-close-1",
		})
	if result := machine.Apply(closeCommand); result.Err() != nil {
		t.Fatal(result.Err())
	}
	closed := machine.State().Sessions[ref.Key()]
	if closed.State != domain.SessionArchived || closed.ArchiveReason != domain.ArchiveManual {
		t.Fatalf("closed session=%#v", closed)
	}
}

func TestSchemaV1SnapshotWithMixedSessionStateRestoresNormalized(t *testing.T) {
	legacy := map[string]any{
		"version": 1,
		"state": map[string]any{
			"schema_version": 1,
			"nodes": map[string]any{
				"alpha": map[string]any{"id": "alpha", "name": "Alpha", "status": "online"},
			},
			"sessions": map[string]any{
				"alpha/legacy": map[string]any{
					"id": "legacy", "node_id": "alpha", "owner_id": 1,
					"name": "Legacy", "state": "idle",
					"created_at": "1970-01-01T00:00:10Z",
				},
			},
			"users": map[string]any{
				"1": map[string]any{"role": "owner", "allowed_nodes": map[string]any{"alpha": true}},
			},
			"grants":      map[string]any{},
			"preferences": map[string]any{},
			"navigation": map[string]any{
				"active_node_by_user":         map[string]any{},
				"active_session_by_user_node": map[string]any{},
			},
		},
		"operation_ledger": map[string]any{},
		"operation_order":  []any{},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(nil)
	if err := machine.RestoreSnapshot(data); err != nil {
		t.Fatal(err)
	}
	session := machine.State().Sessions["alpha/legacy"]
	if session.State != domain.SessionLive || session.RuntimePhase != domain.RuntimeIdle ||
		session.Revision != 1 || session.RuntimeGeneration != 1 {
		t.Fatalf("restored session=%#v", session)
	}
}

func sessionMachine(t *testing.T) (*clusterstate.Machine, domain.SessionRef) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "session", NodeID: "alpha", OwnerID: 1, Name: "Session",
		Backend: "codex", ProviderSessionID: "provider", State: domain.SessionLive,
		CreatedAt: time.Unix(10, 0).UTC(), LiveSinceAt: time.Unix(10, 0).UTC(),
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	return clusterstate.NewMachine(state), session.Ref()
}
