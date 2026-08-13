package domain_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestRuntimePhaseIsIndependentFromLifecycle(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "working", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	result := &domain.SessionOperationResult{
		OperationID: "input-1",
		Action:      domain.ActionSendInput,
		Status:      domain.OperationSucceeded,
	}
	at := time.Unix(20, 0).UTC()
	if err := state.PublishSessionRuntime(
		ref, session.RuntimeGeneration, domain.RuntimeRunning, result, at,
	); err != nil {
		t.Fatal(err)
	}

	session = state.Sessions[ref.Key()]
	if session.State != domain.SessionLive || session.RuntimePhase != domain.RuntimeRunning {
		t.Fatalf("session=%#v", session)
	}
	if session.LastOperation == nil || session.LastOperation.At != at || session.Revision != 2 {
		t.Fatalf("runtime publication=%#v", session)
	}
}

func TestClearResetsNamingAndRejectsOldRuntimeGeneration(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "clear-me", "alpha", 1, time.Unix(10, 0).UTC())
	before := state.Sessions[ref.Key()]
	if err := state.ClearSession(1, ref, before.Revision, "clear-1", time.Unix(20, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	after := state.Sessions[ref.Key()]
	if after.Name != "" || after.ProviderSessionID != "" {
		t.Fatalf("clear retained identity: %#v", after)
	}
	if after.Backend != before.Backend || after.Workdir != before.Workdir {
		t.Fatalf("clear reset stable binding: %#v", after)
	}
	if after.RuntimeGeneration != before.RuntimeGeneration+1 || after.RuntimePhase != domain.RuntimeIdle {
		t.Fatalf("clear generation=%#v", after)
	}
	if err := state.PublishSessionRuntime(
		ref, before.RuntimeGeneration, domain.RuntimeRunning, nil, time.Unix(21, 0).UTC(),
	); !errors.Is(err, domain.ErrStaleOperation) {
		t.Fatalf("old generation error=%v", err)
	}
}

func TestSharedControlMayStopButCannotClearOrClose(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "shared-control", "alpha", 1, time.Unix(10, 0).UTC())
	if err := state.ShareSession(1, ref, 2, domain.ShareControl); err != nil {
		t.Fatal(err)
	}
	for _, action := range []domain.SessionAction{
		domain.ActionSendInput,
		domain.ActionSendKey,
		domain.ActionCapture,
		domain.ActionStop,
	} {
		if !state.CanPerformSessionAction(2, ref, action) {
			t.Errorf("control grant denied %q", action)
		}
	}
	for _, action := range []domain.SessionAction{
		domain.ActionClear,
		domain.ActionClose,
		domain.ActionArchive,
	} {
		if state.CanPerformSessionAction(2, ref, action) {
			t.Errorf("control grant allowed %q", action)
		}
	}
	if err := state.CloseSession(2, ref, state.Sessions[ref.Key()].Revision, "archive-shared", time.Unix(20, 0).UTC()); !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("shared close error=%v", err)
	}
}

func TestCloseSelectsEachUsersMostRecentAvailableBackgroundSession(t *testing.T) {
	state := fixtureState(t)
	created := time.Unix(10, 0).UTC()
	closing := addSession(t, state, "closing", "alpha", 1, created)
	backgroundA := addSession(t, state, "background-a", "alpha", 1, created.Add(time.Second))
	backgroundB := addSession(t, state, "background-b", "alpha", 1, created.Add(2*time.Second))
	for _, ref := range []domain.SessionRef{closing, backgroundA, backgroundB} {
		if err := state.ShareSession(1, ref, 2, domain.ShareControl); err != nil {
			t.Fatal(err)
		}
	}

	// The owner last used A, while the recipient last used B. Both return to
	// the closing session before it is closed.
	selectAt(t, state, 1, backgroundA, 20)
	selectAt(t, state, 1, closing, 30)
	selectAt(t, state, 2, backgroundB, 25)
	selectAt(t, state, 2, closing, 35)

	revision := state.Sessions[closing.Key()].Revision
	if err := state.CloseSession(1, closing, revision, "archive-closing", time.Unix(40, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if got := activeRef(state, 1); got != backgroundA {
		t.Fatalf("owner fallback=%v, want %v", got, backgroundA)
	}
	if got := activeRef(state, 2); got != backgroundB {
		t.Fatalf("recipient fallback=%v, want %v", got, backgroundB)
	}
	closed := state.Sessions[closing.Key()]
	if closed.State != domain.SessionArchived || closed.ArchiveReason != domain.ArchiveManual {
		t.Fatalf("closed session=%#v", closed)
	}
}

func TestCloseKeepsSelectedNodeWhenOnlyAnotherNodeHasLiveSession(t *testing.T) {
	state := fixtureState(t)
	closing := addSession(t, state, "closing", "alpha", 1, time.Unix(10, 0).UTC())
	addSession(t, state, "replacement", "beta", 1, time.Unix(11, 0).UTC())
	if err := state.CloseSession(
		1, closing, state.Sessions[closing.Key()].Revision, "archive-closing", time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[1]; got != "alpha" {
		t.Fatalf("selected node=%q, want alpha", got)
	}
	if got := state.Navigation.ActiveSessionByUserNode[1]["alpha"]; got != "" {
		t.Fatalf("closed node session=%q, want empty", got)
	}
}

func TestLostIsDerivedOnlyFromUnavailableOriginNode(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "live", "alpha", 1, time.Unix(10, 0).UTC())
	if state.IsSessionLost(ref) {
		t.Fatal("online session reported lost")
	}
	node := state.Nodes["alpha"]
	node.Status = domain.NodeOffline
	state.Nodes["alpha"] = node
	if !state.IsSessionLost(ref) {
		t.Fatal("offline origin did not report session lost")
	}
	node.Status = domain.NodeOnline
	state.Nodes["alpha"] = node
	if err := state.MarkMissingOnSameBoot(
		ref, "missing-live", 0, 0, false, time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	session := state.Sessions[ref.Key()]
	if state.IsSessionLost(ref) || session.State != domain.SessionArchived ||
		session.ArchiveReason != domain.ArchiveResumeFailed {
		t.Fatalf("missing runtime session=%#v", session)
	}
}

func TestLegacySessionJSONNormalizesWithoutSchemaBump(t *testing.T) {
	state := fixtureState(t)
	var legacy domain.Session
	if err := json.Unmarshal([]byte(`{
		"id":"legacy","node_id":"alpha","owner_id":1,"name":"Legacy",
		"state":"recovering","created_at":"1970-01-01T00:00:10Z"
	}`), &legacy); err != nil {
		t.Fatal(err)
	}
	state.Sessions[legacy.Ref().Key()] = legacy
	normalized := state.Clone().Sessions[legacy.Ref().Key()]
	if normalized.State != domain.SessionLive || normalized.RuntimePhase != domain.RuntimeDegraded ||
		!normalized.ResumePending || normalized.Revision != 1 || normalized.RuntimeGeneration != 1 {
		t.Fatalf("normalized legacy session=%#v", normalized)
	}
}

func selectAt(
	t *testing.T,
	state *domain.State,
	userID domain.UserID,
	ref domain.SessionRef,
	unix int64,
) {
	t.Helper()
	if err := state.SelectSession(userID, ref, time.Unix(unix, 0).UTC()); err != nil {
		t.Fatal(err)
	}
}

func activeRef(state *domain.State, userID domain.UserID) domain.SessionRef {
	nodeID := state.Navigation.ActiveNodeByUser[userID]
	return domain.SessionRef{
		NodeID:    nodeID,
		SessionID: state.Navigation.ActiveSessionByUserNode[userID][nodeID],
	}
}
