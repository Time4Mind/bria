package domain_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestUpdateNodeRuntimeReplacesCapabilitiesAtomically(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0).UTC()
	backends := []domain.BackendDescriptor{{
		Name: "codex", Version: "1", Capabilities: []string{"session.resume"},
	}}
	if err := state.UpdateNodeRuntime("node", domain.NodeOnline, "v1", backends, at); err != nil {
		t.Fatal(err)
	}
	backends[0].Capabilities[0] = "mutated"
	node := state.Nodes["node"]
	if node.Status != domain.NodeOnline || node.Version != "v1" ||
		node.Backends[0].Capabilities[0] != "session.resume" || !node.LastSeenAt.Equal(at) {
		t.Fatalf("updated node=%#v", node)
	}
	before := node
	if err := state.UpdateNodeRuntime("node", domain.NodeOnline, "v2", []domain.BackendDescriptor{
		{Name: "codex"}, {Name: "codex"},
	}, at); err == nil {
		t.Fatal("duplicate backend accepted")
	}
	if got := state.Nodes["node"]; got.Version != before.Version || len(got.Backends) != 1 {
		t.Fatalf("invalid update mutated node=%#v", got)
	}
}

func TestHeartbeatKeepsInstalledBackendsSeparateFromExplicitConnections(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node"}); err != nil {
		t.Fatal(err)
	}
	installed := []domain.BackendDescriptor{{
		Name: "CODEX", Version: "1", Capabilities: []string{"session.create"},
	}}
	at := time.Unix(100, 0).UTC()
	if err := state.UpdateNodeInventory("node", domain.NodeOnline, "v1", installed, at); err != nil {
		t.Fatal(err)
	}
	node := state.Nodes["node"]
	if len(node.InstalledBackends) != 1 || len(node.Backends) != 0 ||
		!node.BackendSelectionInitialized {
		t.Fatalf("unexpected inventory=%#v", node)
	}
	if err := state.SetNodeBackendConnected("node", "codex", true); err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateNodeInventory("node", domain.NodeOnline, "v2", installed, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	node = state.Nodes["node"]
	if len(node.Backends) != 1 || node.Backends[0].Name != "codex" {
		t.Fatalf("connected backend not retained=%#v", node.Backends)
	}
	if err := state.SetNodeBackendConnected("node", "codex", false); err != nil {
		t.Fatal(err)
	}
	if len(state.Nodes["node"].Backends) != 0 {
		t.Fatal("disconnected backend remained available")
	}
}

func TestHeartbeatAndOfflineTimeoutPreserveLastEvidence(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	seenAt := time.Unix(100, 0).UTC()
	plan, err := state.PublishNodeHeartbeat("node", "boot", "v1", "", "", nil, nil, nil, nil, seenAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Recover) != 0 || state.Nodes["node"].Status != domain.NodeOnline {
		t.Fatalf("heartbeat plan=%#v node=%#v", plan, state.Nodes["node"])
	}
	if err := state.MarkNodeOffline("node", seenAt.Add(-time.Second)); err != domain.ErrStaleOperation {
		t.Fatalf("stale timeout error=%v", err)
	}
	if err := state.MarkNodeOffline("node", seenAt); err != nil {
		t.Fatal(err)
	}
	node := state.Nodes["node"]
	if node.Status != domain.NodeOffline || !node.LastSeenAt.Equal(seenAt) {
		t.Fatalf("offline node=%#v", node)
	}
}

func TestHeartbeatPinsAndRotatesNodeCertificateFingerprint(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	at := time.Unix(100, 0).UTC()
	if _, err := state.PublishNodeHeartbeat(
		"node", "boot", "v1", first, "", nil, nil, nil, nil, at,
	); err != nil {
		t.Fatal(err)
	}
	if got := state.Nodes["node"].Fingerprint; got != first {
		t.Fatalf("pinned fingerprint=%q", got)
	}
	if _, err := state.PublishNodeHeartbeat(
		"node", "boot", "v1", second, strings.Repeat("c", 64), nil, nil, nil, nil,
		at.Add(time.Second),
	); err == nil {
		t.Fatal("unlinked certificate rotation accepted")
	}
	if got := state.Nodes["node"].Fingerprint; got != first {
		t.Fatalf("rejected rotation changed fingerprint=%q", got)
	}
	if _, err := state.PublishNodeHeartbeat(
		"node", "boot", "v1", second, first, nil, nil, nil, nil, at.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if got := state.Nodes["node"].Fingerprint; got != second {
		t.Fatalf("rotated fingerprint=%q", got)
	}
}

func TestHeartbeatPublishesAndClearsBoundedInteractivePrompt(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(10, 0).UTC()
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Work", Backend: "codex",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeRunning,
		CreatedAt: created, LiveSinceAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	report := domain.InteractivePromptReport{
		SessionID: "session", Generation: 1, Present: true,
		Kind: "codex_approval", Hash: "0123456789abcdef0123456789abcdef",
	}
	if _, err := state.PublishNodeHeartbeat(
		"node", "boot", "v1", "", "", nil, nil, []domain.InteractivePromptReport{report}, nil, created,
	); err != nil {
		t.Fatal(err)
	}
	session := state.Sessions["node/session"]
	if session.RuntimePhase != domain.RuntimeWaitingInput || session.InteractivePrompt == nil ||
		session.InteractivePrompt.Hash != report.Hash {
		t.Fatalf("interactive session=%#v", session)
	}
	report.Present, report.Kind, report.Hash = false, "", ""
	if _, err := state.PublishNodeHeartbeat(
		"node", "boot", "v1", "", "", nil, nil, []domain.InteractivePromptReport{report}, nil, created.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	session = state.Sessions["node/session"]
	if session.RuntimePhase != domain.RuntimeRunning || session.InteractivePrompt != nil {
		t.Fatalf("cleared interactive session=%#v", session)
	}
}

func TestInvalidInteractiveHeartbeatDoesNotPartiallyMutateNode(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	before := state.Nodes["node"]
	_, err := state.PublishNodeHeartbeat(
		"node", "boot", "changed", "", "", nil, nil, []domain.InteractivePromptReport{{
			SessionID: "session", Generation: 1, Present: true, Kind: "permission", Hash: "bad",
		}}, nil, time.Unix(100, 0).UTC(),
	)
	if err == nil || !reflect.DeepEqual(state.Nodes["node"], before) {
		t.Fatalf("err=%v node=%#v", err, state.Nodes["node"])
	}
}

func TestHeartbeatReconcilesMissedTranscriptFinalWithoutTranscriptBody(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Session",
		Backend: "codex", Workdir: "/workspace", ProviderSessionID: "provider",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeRunning,
		RuntimeGeneration: 3, CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	finalAt := created.Add(time.Minute)
	report := domain.TranscriptFinalReport{
		SessionID: "session", Generation: 3, Timestamp: finalAt,
		Digest: strings.Repeat("a", 64),
	}
	if _, err := state.PublishNodeHeartbeat(
		"node", "boot", "v1", "", "", nil, nil, nil, []domain.TranscriptFinalReport{report},
		finalAt.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	session := state.Sessions["node/session"]
	if session.RuntimePhase != domain.RuntimeIdle || session.LastEventAt != finalAt || session.Revision != 2 {
		t.Fatalf("settled session=%#v", session)
	}
	if _, err := state.PublishNodeHeartbeat(
		"node", "boot", "v1", "", "", nil, nil, nil, []domain.TranscriptFinalReport{report},
		finalAt.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if got := state.Sessions["node/session"].Revision; got != session.Revision {
		t.Fatalf("replayed final advanced revision to %d", got)
	}
}

func TestHeartbeatIgnoresTranscriptFinalForWrongGenerationOrOldTimestamp(t *testing.T) {
	for _, report := range []domain.TranscriptFinalReport{
		{SessionID: "session", Generation: 2, Timestamp: time.Unix(200, 0).UTC(), Digest: strings.Repeat("b", 64)},
		{SessionID: "session", Generation: 3, Timestamp: time.Unix(90, 0).UTC(), Digest: strings.Repeat("c", 64)},
	} {
		state := transcriptFinalState(t)
		if _, err := state.PublishNodeHeartbeat(
			"node", "boot", "v1", "", "", nil, nil, nil, []domain.TranscriptFinalReport{report},
			time.Unix(210, 0).UTC(),
		); err != nil {
			t.Fatal(err)
		}
		if state.Sessions["node/session"].RuntimePhase != domain.RuntimeRunning {
			t.Fatalf("report %#v settled session", report)
		}
	}
}

func transcriptFinalState(t *testing.T) *domain.State {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Session",
		Backend: "codex", Workdir: "/workspace", ProviderSessionID: "provider",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeRunning,
		RuntimeGeneration: 3, CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestOfflineActiveNodePromotesNewestEligibleBackground(t *testing.T) {
	state := domain.NewState()
	seenAt := time.Unix(100, 0).UTC()
	for _, node := range []domain.Node{
		{ID: "first", Name: "First", Status: domain.NodeOnline, LastSeenAt: seenAt},
		{ID: "second", Name: "Second", Status: domain.NodeOnline, LastSeenAt: seenAt},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "first", "second"); err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range []domain.NodeID{"first", "second"} {
		at := time.Unix(int64(index+1), 0).UTC()
		if err := state.AddSession(domain.Session{
			ID: domain.SessionID("session-" + string(nodeID)), NodeID: nodeID,
			OwnerID: 7, Name: string(nodeID), Backend: "claude",
			State: domain.SessionLive, CreatedAt: at, LiveSinceAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	first := domain.SessionRef{NodeID: "first", SessionID: "session-first"}
	if err := state.SelectSession(7, first, time.Unix(10, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkNodeOffline("first", seenAt); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[7]; got != "first" {
		t.Fatalf("transient outage changed selected node=%q", got)
	}
	if err := state.MarkNodeOffline("second", seenAt); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[7]; got != "first" {
		t.Fatalf("selected offline node was discarded=%q", got)
	}
	if got := state.Navigation.ActiveSessionByUserNode[7]["first"]; got != "session-first" {
		t.Fatalf("selected offline session was discarded=%q", got)
	}
}

func TestOnlineHeartbeatDoesNotReplaceExplicitlySelectedEmptyNode(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "empty", Name: "Empty", Status: domain.NodeOnline},
		{ID: "busy", Name: "Busy", Status: domain.NodeOnline},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "empty", "busy"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	if err := state.AddSession(domain.Session{ID: "live", NodeID: "busy", OwnerID: 7,
		Backend: "codex", State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: created, LiveSinceAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectNode(7, "empty", created.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateNodeInventory("busy", domain.NodeOnline, "v", nil, created.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[7]; got != "empty" {
		t.Fatalf("selected node changed to %q", got)
	}
}
