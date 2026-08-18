package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type deadlineControls struct{ SessionControls }

func (deadlineControls) Transcript(
	ctx context.Context, _ application.Principal, _ domain.SessionRef,
) ([]transcript.Event, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCardCacheIsBoundedAndPinsActiveSession(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(10, 0).UTC()
	active := domain.Session{
		ID: "active", NodeID: "node", OwnerID: 7, Name: "Active", Backend: "codex",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: created, LiveSinceAt: created,
	}
	if err := state.AddSession(active); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(7, active.Ref(), created); err != nil {
		t.Fatal(err)
	}
	port := &clusterLogPort{state: state}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{service: service, cardRuntimeState: newCardRuntimeState()}
	handler.rememberCardTranscript(active.Ref(), 1, "active-provider", []transcript.Event{{Text: "active"}})
	for index := 0; index < maxCachedCardSessions; index++ {
		ref := domain.SessionRef{NodeID: "node", SessionID: domain.SessionID(fmt.Sprintf("old-%02d", index))}
		handler.rememberCardTranscript(ref, 1, string(ref.SessionID), []transcript.Event{{Text: string(ref.SessionID)}})
	}
	if len(handler.cardTranscripts) != maxCachedCardSessions || len(handler.cardContexts) != maxCachedCardSessions {
		t.Fatalf("cache sizes transcripts=%d contexts=%d", len(handler.cardTranscripts), len(handler.cardContexts))
	}
	if _, ok := handler.cachedCardTranscript(active.Ref()); !ok {
		t.Fatal("active session was evicted")
	}
	oldest := domain.SessionRef{NodeID: "node", SessionID: "old-00"}
	if _, ok := handler.cachedCardTranscript(oldest); ok {
		t.Fatal("oldest unpinned session was not evicted")
	}
	if handler.cardEvictions != 1 {
		t.Fatalf("evictions=%d, want 1", handler.cardEvictions)
	}
}

func TestBackgroundTranscriptReadHasIndependentDeadline(t *testing.T) {
	previous := backgroundTranscriptBudget
	backgroundTranscriptBudget = 10 * time.Millisecond
	t.Cleanup(func() { backgroundTranscriptBudget = previous })
	handler := &Handler{
		controls: deadlineControls{}, cardRuntimeState: newCardRuntimeState(),
	}
	_, err := handler.readBackgroundTranscript(
		context.Background(), application.Principal{UserID: 7},
		domain.SessionRef{NodeID: "node", SessionID: "session"},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if handler.transcriptReads != 1 || handler.transcriptSlow != 1 || handler.transcriptMax <= 0 {
		t.Fatalf("timing stats reads=%d slow=%d max=%s",
			handler.transcriptReads, handler.transcriptSlow, handler.transcriptMax)
	}
}
