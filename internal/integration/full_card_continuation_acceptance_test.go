package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramstate"
)

type fullCardAcceptedObserver struct{ calls int }

func (observer *fullCardAcceptedObserver) ObserveAcceptedWithCallbacks(_ context.Context, _ domain.SessionID, _ domain.ProviderBinding, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	observer.calls++
	if err := callbacks.OnEvent(sessionruntime.TurnEvent{ID: "offset:1", Kind: sessionruntime.EventCommentary, Text: "progress"}); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "answer"}, nil
}

func TestFullCardAcceptedContinuationKeepsObserverAndCompletesOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const id domain.SessionID = "cccccccc-cccc-4ccc-9ccc-cccccccccccc"
	starting, err := domain.NewStartingSession(id, "intent-full-card", "local", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native-full-card", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, ready, running); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card := telegramstate.Card{
			SessionID: id, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true},
			History: make([]string, 512), HistoryKeys: make([]string, 512), HistoryKinds: make([]string, 512), HistoryTurnKeys: make([]string, 512),
		}
		for index := 0; index < 510; index++ {
			card.History[index], card.HistoryKinds[index] = fmt.Sprintf("old-%03d", index), "commentary"
		}
		card.History[510], card.HistoryKeys[510], card.HistoryKinds[510] = "accepted prompt", "accepted", "prompt"
		card.History[511], card.HistoryKeys[511], card.HistoryKinds[511] = "queued prompt", "queued", "prompt"
		return state.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	binding, _ := running.Binding()
	observer := &fullCardAcceptedObserver{}
	turns, err := app.NewSessionTurnLifecycle(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := telegramcontroller.New(42, 42, "local", staticCreator{session: running}, store,
		&capturingSubmitter{calls: make(chan submittedTurn, 1)}, discardNotifier{},
		telegramcontroller.Options{UIState: store, AcceptedObserver: observer, TurnLifecycle: turns})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 2)
	if err := c.ContinueAcceptedInput(ctx, binding, telegramcontroller.DurableLeasedInput{SessionID: id, MessageID: "accepted", Sequence: 1}, telegramcontroller.DurableInputCallbacks{
		OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
			completed <- receipt
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case receipt := <-completed:
		if receipt.Completion != telegramcontroller.DurableInputSucceeded {
			t.Fatalf("full-card completion = %+v", receipt)
		}
	case <-ctx.Done():
		t.Fatal("full-card continuation did not complete")
	}
	for {
		session, loadErr := store.Load(ctx, id)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if session.Status() == domain.SessionReady {
			break
		}
		if session.Status() == domain.SessionAwaitingRecovery {
			t.Fatal("full-card continuation entered awaiting_recovery")
		}
		select {
		case <-ctx.Done():
			t.Fatalf("full-card session remained %s", session.Status())
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case duplicate := <-completed:
		t.Fatalf("accepted input completed twice: %+v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
	if observer.calls != 1 {
		t.Fatalf("observer calls = %d", observer.calls)
	}
	snapshot, err := store.LoadTelegramUI(ctx)
	card, ok := snapshot.Card(id)
	if err != nil || !ok || len(card.History) != 512 || countFullCardText(card.History, "progress") != 1 || countFullCardText(card.History, "answer") != 1 {
		t.Fatalf("full-card result: card=%#v found=%t err=%v", card, ok, err)
	}
}

func countFullCardText(history []string, want string) int {
	count := 0
	for _, item := range history {
		if item == want {
			count++
		}
	}
	return count
}
