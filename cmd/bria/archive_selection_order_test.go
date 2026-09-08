package main

import (
	"context"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

type trackedArchiveCompletion struct {
	*app.SessionCloser
	done chan struct{}
}

func (c trackedArchiveCompletion) BeginCloseWithCompletion(ctx context.Context, id domain.SessionID, completed func(context.Context, app.CloseSessionResult, error)) (app.CloseSessionResult, error) {
	return c.SessionCloser.BeginCloseWithCompletion(ctx, id, func(ctx context.Context, result app.CloseSessionResult, err error) {
		completed(ctx, result, err)
		close(c.done)
	})
}

func TestArchiveCompletionPreservesNewerManualSelection(t *testing.T) {
	ctx := context.Background()
	s, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222", "33333333-3333-4333-9333-333333333333")
	release, done := make(chan struct{}), make(chan struct{})
	closer, err := app.NewSessionCloser(s, heldArchiveStarter{release}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c := archiveController(t, s, nil, telegramcontroller.Options{Recovered: sessions, UIState: s, SessionCloser: trackedArchiveCompletion{closer, done}})
	defer c.Close(ctx)
	selectSession := func(id domain.SessionID) {
		t.Helper()
		if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	selectSession(sessions[1].ID())
	selectSession(sessions[0].ID())
	initial, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: sessions[0].ID(), Choice: 1})
	if err != nil || initial.Card == nil || initial.Card.SessionID != sessions[1].ID() {
		close(release)
		t.Fatalf("initial fallback=%+v err=%v", initial.Card, err)
	}
	selectSession(sessions[2].ID())
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completion not reconciled")
	}
	active, err := s.LoadActiveSession(ctx)
	if err != nil || active != sessions[2].ID() {
		t.Fatalf("late completion replaced newer selection: %q err=%v", active, err)
	}
	result, err := c.ProjectCurrent(ctx, sessions[2].ID())
	if err != nil || result.Card == nil || result.Card.SessionID != sessions[2].ID() {
		t.Fatalf("current projection=%+v err=%v", result, err)
	}
}
