package nodecontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
	"github.com/Time4Mind/bria/internal/transcript"
)

type transcriptSourceRecorder struct {
	request transcript.Request
	events  []transcript.Event
	first   string
}

type archivedTranscriptRecorder struct {
	session domain.Session
	events  []transcript.Event
}

func (r *archivedTranscriptRecorder) ReadArchivedTranscript(
	_ context.Context,
	session domain.Session,
) ([]transcript.Event, error) {
	r.session = session
	return append([]transcript.Event(nil), r.events...), nil
}

func (r *transcriptSourceRecorder) Read(
	_ context.Context,
	request transcript.Request,
) ([]transcript.Event, error) {
	r.request = request
	return append([]transcript.Event(nil), r.events...), nil
}

func (r *transcriptSourceRecorder) ReadFirstUserText(
	_ context.Context,
	request transcript.Request,
) (string, error) {
	r.request = request
	return r.first, nil
}

func TestLocalTranscriptServiceResolvesTrustedSessionMetadata(t *testing.T) {
	state, session := transcriptState(t)
	source := &transcriptSourceRecorder{events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal, Text: "done",
	}}}
	service, err := NewLocalTranscriptService("node", staticState{state}, source)
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.ReadTranscript(context.Background(), TranscriptQuery{
		ActorID: 1, NodeID: "node", SessionID: "session",
		ExpectedGeneration: session.RuntimeGeneration,
	})
	if err != nil || len(events) != 1 || events[0].Text != "done" {
		t.Fatalf("events=(%#v, %v)", events, err)
	}
	want := transcript.Request{
		Backend: transcript.BackendClaude, ProviderSessionID: "provider-id", Workdir: "/workspace",
	}
	if source.request != want {
		t.Fatalf("source request=%#v, want %#v", source.request, want)
	}
}

func TestLocalTranscriptServiceReadsActualFirstUserPrompt(t *testing.T) {
	state, session := transcriptState(t)
	source := &transcriptSourceRecorder{first: "actual first prompt"}
	service, err := NewLocalTranscriptService("node", staticState{state}, source)
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.ReadTranscript(context.Background(), TranscriptQuery{
		ActorID: 1, NodeID: "node", SessionID: "session",
		ExpectedGeneration: session.RuntimeGeneration, FirstUserPrompt: true,
	})
	if err != nil || len(events) != 1 || events[0].Kind != transcript.EventUserText ||
		events[0].Text != "actual first prompt" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestLocalTranscriptServiceRejectsUnauthorizedOrStaleQueries(t *testing.T) {
	state, session := transcriptState(t)
	source := &transcriptSourceRecorder{}
	service, err := NewLocalTranscriptService("node", staticState{state}, source)
	if err != nil {
		t.Fatal(err)
	}
	queries := []TranscriptQuery{
		{ActorID: 2, NodeID: "node", SessionID: "session", ExpectedGeneration: session.RuntimeGeneration},
		{ActorID: 1, NodeID: "other", SessionID: "session", ExpectedGeneration: session.RuntimeGeneration},
		{ActorID: 1, NodeID: "node", SessionID: "session", ExpectedGeneration: session.RuntimeGeneration + 1},
	}
	for _, query := range queries {
		if _, err := service.ReadTranscript(context.Background(), query); err == nil {
			t.Fatalf("unauthorized query accepted: %#v", query)
		}
	}
	if source.request != (transcript.Request{}) {
		t.Fatalf("unauthorized query reached source: %#v", source.request)
	}
}

func TestLocalTranscriptServiceReadsReadyNativeArchive(t *testing.T) {
	state, session := transcriptState(t)
	session.State = domain.SessionArchived
	session.ArchiveID = "archive-id"
	session.ArchiveReady = true
	state.Sessions[session.Ref().Key()] = session
	live := &transcriptSourceRecorder{}
	archives := &archivedTranscriptRecorder{events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal, Text: "archived",
	}}}
	service, err := NewLocalTranscriptService("node", staticState{state}, live, archives)
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.ReadTranscript(context.Background(), TranscriptQuery{
		ActorID: 1, NodeID: "node", SessionID: "session",
		ExpectedGeneration: session.RuntimeGeneration,
	})
	if err != nil || len(events) != 1 || events[0].Text != "archived" ||
		archives.session.Ref() != session.Ref() || live.request != (transcript.Request{}) {
		t.Fatalf("events=%#v archived=%#v live=%#v err=%v",
			events, archives.session, live.request, err)
	}
}

func TestTranscriptEndpointAcceptsOnlyCurrentLeader(t *testing.T) {
	state, session := transcriptState(t)
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	ca, _, _, err := security.GenerateCA("cluster", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	peer := issueTLSCertificate(t, ca, "cluster", "node")
	reader := &transcriptReaderStub{}
	server := &Server{
		nodeID: "node", clusterID: "cluster", leadership: staticLeadership("replacement"),
		membership: guard, transcripts: reader,
	}
	query := TranscriptQuery{
		ActorID: 1, NodeID: "node", SessionID: "session",
		ExpectedGeneration: session.RuntimeGeneration,
	}
	request := transcriptHTTPRequest(t, query, &tls.ConnectionState{
		PeerCertificates: peer.LeafCertificate,
	})
	response := httptest.NewRecorder()
	server.handleTranscript(response, request)
	if response.Code != http.StatusConflict || reader.calls != 0 {
		t.Fatalf("status=%d transcript calls=%d", response.Code, reader.calls)
	}
}

type transcriptReaderStub struct{ calls int }

func (r *transcriptReaderStub) ReadTranscript(
	context.Context,
	TranscriptQuery,
) ([]transcript.Event, error) {
	r.calls++
	return nil, nil
}

func transcriptHTTPRequest(
	t *testing.T,
	query TranscriptQuery,
	connection *tls.ConnectionState,
) *http.Request {
	t.Helper()
	body, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, transcriptPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = connection
	return request
}

func transcriptState(t *testing.T) (*domain.State, domain.Session) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "session", NodeID: "node", OwnerID: 1, Name: "Session",
		Backend: "claude", ProviderSessionID: "provider-id", Workdir: "/workspace",
		State: domain.SessionLive, CreatedAt: time.Unix(1, 0).UTC(),
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	return state, state.Sessions[session.Ref().Key()]
}
