package main

import (
	"context"
	"slices"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

func TestRecoveryCardRemainsVisibleAndRejectsOrdinaryInput(t *testing.T) {
	ctx := context.Background()
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222")
	lost := sessions[0]
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: sessions, UIState: store})
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: lost.ID()}); err != nil {
		t.Fatal(err)
	}
	awaiting, err := lost.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Replace(ctx, lost, awaiting); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		result, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSessions})
		if err != nil || result.Card == nil || result.Card.SessionID != lost.ID() || !slices.Contains(result.Card.SelectableSessionIDs, lost.ID()) {
			t.Fatalf("recovery disappeared pass=%d result=%+v err=%v", pass, result, err)
		}
		before, _ := store.LoadCardHistory(ctx, lost.ID())
		_, err = c.HandleSemanticMessage(ctx, coordinator.Update{ID: int64(900 + pass), Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "do not accept in recovery"})
		if err != nil {
			t.Fatal(err)
		}
		after, _ := store.LoadCardHistory(ctx, lost.ID())
		if len(after) != len(before) {
			t.Fatal("ordinary input entered recovering history")
		}
		if err = c.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if pass == 0 {
			c = archiveController(t, store, nil, telegramcontroller.Options{Recovered: []domain.Session{sessions[1]}, UIState: store})
		}
	}
}
