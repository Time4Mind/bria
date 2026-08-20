package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type transcriptReconcileReader struct {
	events []transcript.Event
	err    error
	calls  int
}

func (r *transcriptReconcileReader) Read(
	context.Context,
	transcript.Request,
) ([]transcript.Event, error) {
	r.calls++
	return append([]transcript.Event(nil), r.events...), r.err
}

func TestCollectTranscriptFinalsExportsOnlyBoundedEvidence(t *testing.T) {
	finalAt := time.Unix(200, 0).UTC()
	state := transcriptReconcileState(t, domain.RuntimeRunning)
	reader := &transcriptReconcileReader{events: []transcript.Event{
		{Kind: transcript.EventUserText, Text: "private prompt", Timestamp: time.Unix(150, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "private answer", Timestamp: finalAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventTurnComplete, Timestamp: finalAt.Add(time.Second).Format(time.RFC3339Nano)},
	}}
	reports := collectTranscriptFinals(context.Background(), "node", state, reader)
	if len(reports) != 1 || reports[0].SessionID != "session" ||
		reports[0].Generation != 3 || reports[0].Timestamp != finalAt ||
		len(reports[0].Digest) != 64 {
		t.Fatalf("reports=%#v", reports)
	}
	encoded, err := json.Marshal(reports)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private prompt") ||
		strings.Contains(string(encoded), "private answer") {
		t.Fatalf("transcript text escaped into heartbeat evidence: %s", encoded)
	}
}

func TestCollectTranscriptFinalsRejectsUnsettledOrUnavailableTranscript(t *testing.T) {
	state := transcriptReconcileState(t, domain.RuntimeRunning)
	reader := &transcriptReconcileReader{events: []transcript.Event{
		{Kind: transcript.EventAssistantFinal, Text: "old", Timestamp: time.Unix(90, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: transcript.EventToolCall, Head: "exec", Timestamp: time.Unix(110, 0).UTC().Format(time.RFC3339Nano)},
	}}
	if reports := collectTranscriptFinals(context.Background(), "node", state, reader); len(reports) != 0 {
		t.Fatalf("unsettled transcript reports=%#v", reports)
	}
	reader.err = transcript.ErrTranscriptNotFound
	if reports := collectTranscriptFinals(context.Background(), "node", state, reader); len(reports) != 0 {
		t.Fatalf("missing transcript reports=%#v", reports)
	}
	reader.err = errors.New("read failed")
	if reports := collectTranscriptFinals(context.Background(), "node", state, reader); len(reports) != 0 {
		t.Fatalf("failed transcript reports=%#v", reports)
	}
}

func TestCollectTranscriptFinalsRejectsAnswerForEarlierQueuedPrompt(t *testing.T) {
	state := transcriptReconcileState(t, domain.RuntimeRunning)
	reader := &transcriptReconcileReader{events: []transcript.Event{
		{Kind: transcript.EventUserText, Text: "earlier prompt",
			Timestamp: time.Unix(90, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "earlier answer",
			Timestamp: time.Unix(110, 0).UTC().Format(time.RFC3339Nano)},
	}}
	if reports := collectTranscriptFinals(context.Background(), "node", state, reader); len(reports) != 0 {
		t.Fatalf("earlier queued turn reports=%#v", reports)
	}
}

func TestCollectTranscriptFinalsAcceptsProviderUserTimestampSkew(t *testing.T) {
	state := transcriptReconcileState(t, domain.RuntimeRunning)
	finalAt := time.Unix(120, 0).UTC()
	reports := collectTranscriptFinals(
		context.Background(), "node", state,
		&transcriptReconcileReader{events: []transcript.Event{
			{Kind: transcript.EventUserText, Timestamp: time.Unix(99, 0).UTC().Format(time.RFC3339Nano)},
			{Kind: transcript.EventAssistantFinal, Timestamp: finalAt.Format(time.RFC3339Nano)},
			{Kind: transcript.EventTurnComplete, Timestamp: finalAt.Add(time.Second).Format(time.RFC3339Nano)},
		}},
	)
	if len(reports) != 1 || reports[0].Timestamp != finalAt {
		t.Fatalf("reports=%#v", reports)
	}
}

func TestCollectTranscriptFinalsRepairsCompletedClaudeLocalCommand(t *testing.T) {
	state := transcriptReconcileState(t, domain.RuntimeRunning)
	session := state.Sessions["node/session"]
	session.Backend = "claude"
	session.LastEventAt = time.Unix(130, 0).UTC()
	state.Sessions["node/session"] = session
	reports := collectTranscriptFinals(
		context.Background(), "node", state,
		&transcriptReconcileReader{events: []transcript.Event{
			{Kind: transcript.EventUserText, Text: "/model", Timestamp: time.Unix(119, 0).UTC().Format(time.RFC3339Nano)},
			{Kind: transcript.EventAssistantFinal, LocalCommand: true, Timestamp: time.Unix(120, 0).UTC().Format(time.RFC3339Nano)},
		}},
	)
	if len(reports) != 1 || reports[0].Timestamp != session.LastEventAt {
		t.Fatalf("local command reports=%#v", reports)
	}
}

func TestCollectTranscriptRuntimeRepairsRecoveredOpenTurn(t *testing.T) {
	state := transcriptReconcileState(t, domain.RuntimeIdle)
	reports := collectTranscriptRuntime(
		context.Background(), "node", state,
		&transcriptReconcileReader{events: []transcript.Event{
			{Kind: transcript.EventTurnComplete, Timestamp: time.Unix(110, 0).UTC().Format(time.RFC3339Nano)},
			{Kind: transcript.EventToolCall, Timestamp: time.Unix(120, 0).UTC().Format(time.RFC3339Nano)},
		}},
	)
	if len(reports) != 1 || reports[0].Phase != domain.RuntimeRunning ||
		reports[0].Generation != 3 {
		t.Fatalf("reports=%#v", reports)
	}
}

func TestCollectTranscriptRuntimeRejectsStaleObservation(t *testing.T) {
	state := transcriptReconcileState(t, domain.RuntimeRunning)
	reports := collectTranscriptRuntime(
		context.Background(), "node", state,
		&transcriptReconcileReader{events: []transcript.Event{{
			Kind:      transcript.EventTurnComplete,
			Timestamp: time.Unix(90, 0).UTC().Format(time.RFC3339Nano),
		}}},
	)
	if len(reports) != 0 {
		t.Fatalf("stale reports=%#v", reports)
	}
}

func transcriptReconcileState(t *testing.T, phase domain.RuntimePhase) *domain.State {
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
		State: domain.SessionLive, RuntimePhase: phase, RuntimeGeneration: 3,
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	return state
}
