package sessioncontrol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

type closeTranscriptStub struct {
	mu     sync.Mutex
	events []transcript.Event
	err    error
	query  nodecontrol.TranscriptQuery
	calls  int
}

func (s *closeTranscriptStub) ReadTranscript(
	_ context.Context,
	query nodecontrol.TranscriptQuery,
) ([]transcript.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.query = query
	return append([]transcript.Event(nil), s.events...), s.err
}

func TestCloseUsesDurableEmptyMarkerWithoutTranscriptRead(t *testing.T) {
	controller, _, machine := controllerFixture(t)
	reader := &closeTranscriptStub{err: errors.New("must not be read")}
	controller.transcripts = reader
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	state := machine.State()
	session := state.Sessions[ref.Key()]
	session.UserRequestTracked = true
	session.UserRequestSeen = false
	state.Sessions[ref.Key()] = session
	machine = clusterstate.NewMachine(state)
	service, err := application.NewService(machinePort{machine}, machinePort{machine})
	if err != nil {
		t.Fatal(err)
	}
	controller.service = service

	if _, err := controller.Close(
		context.Background(), application.Principal{UserID: 7}, "tracked-empty", ref,
	); err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	calls := reader.calls
	reader.mu.Unlock()
	if calls != 0 {
		t.Fatalf("transcript reads=%d, want 0", calls)
	}
	if got := machine.State().Sessions[ref.Key()].State; got != domain.SessionDiscarding {
		t.Fatalf("tracked empty state=%q", got)
	}
}

func TestCloseDiscardsSessionWithoutUserRequest(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	reader := &closeTranscriptStub{}
	controller.transcripts = reader
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}

	accepted, err := controller.Close(
		context.Background(), application.Principal{UserID: 7}, "empty-close", ref,
	)
	if err != nil || accepted.Session != ref {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	if session := machine.State().Sessions[ref.Key()]; session.State != domain.SessionDiscarding {
		t.Fatalf("empty close state=%#v", session)
	}
	if got := machine.State().VisibleSessions(7, false); len(got) != 0 {
		t.Fatalf("empty close leaked into archive=%#v", got)
	}
	runtime.mu.Lock()
	request := runtime.requests[len(runtime.requests)-1]
	runtime.results[request.OperationID] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	if request.Action != runtimehost.ActionDiscard || request.Archive != nil ||
		request.ArchiveCommitID != "" {
		t.Fatalf("discard request=%#v", request)
	}
	reader.mu.Lock()
	query := reader.query
	reader.mu.Unlock()
	if !query.FirstUserPrompt {
		t.Fatalf("close read full transcript: %#v", query)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := machine.State().Sessions[ref.Key()]; !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("discarded session record was not removed")
}

func TestCloseArchivesSessionWithUserRequest(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	controller.transcripts = &closeTranscriptStub{events: []transcript.Event{{
		Kind: transcript.EventUserText, Text: "do the work",
	}}}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if _, err := controller.Close(
		context.Background(), application.Principal{UserID: 7}, "used-close", ref,
	); err != nil {
		t.Fatal(err)
	}
	session := machine.State().Sessions[ref.Key()]
	if session.State != domain.SessionArchived || session.ArchiveID != "archive-used-close" {
		t.Fatalf("used close state=%#v", session)
	}
	runtime.mu.Lock()
	request := runtime.requests[len(runtime.requests)-1]
	runtime.mu.Unlock()
	if request.Action != runtimehost.ActionClose || request.Archive == nil {
		t.Fatalf("used close request=%#v", request)
	}
}

func TestCloseArchivesWhenTranscriptCannotBeProvedEmpty(t *testing.T) {
	controller, _, machine := controllerFixture(t)
	controller.transcripts = &closeTranscriptStub{err: errors.New("node unavailable")}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if _, err := controller.Close(
		context.Background(), application.Principal{UserID: 7}, "safe-close", ref,
	); err != nil {
		t.Fatal(err)
	}
	if session := machine.State().Sessions[ref.Key()]; session.State != domain.SessionArchived {
		t.Fatalf("uncertain close discarded data=%#v", session)
	}
}

func TestQueuedFirstRequestPreventsDiscardBeforeTranscriptExists(t *testing.T) {
	controller, _, machine := controllerFixture(t)
	controller.transcripts = &closeTranscriptStub{}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	state := machine.State()
	session := state.Sessions[ref.Key()]
	session.ProviderSessionID = ""
	session.LastOperation = &domain.SessionOperationResult{
		OperationID: "first", Action: domain.ActionSendInput, Status: domain.OperationQueued,
	}
	state.Sessions[ref.Key()] = session
	machine = clusterstate.NewMachine(state)
	service, err := application.NewService(machinePort{machine}, machinePort{machine})
	if err != nil {
		t.Fatal(err)
	}
	controller.service = service
	if _, err := controller.Close(
		context.Background(), application.Principal{UserID: 7}, "queued-close", ref,
	); err != nil {
		t.Fatal(err)
	}
	if got := machine.State().Sessions[ref.Key()].State; got != domain.SessionArchived {
		t.Fatalf("queued first request state=%q", got)
	}
}
