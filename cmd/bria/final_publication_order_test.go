package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcompletioncomposition"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegrampipeline"
	"bria/internal/telegrampromptcomposition"
	"bria/internal/telegramruntimecomposition"
)

type publicationWriteStore struct {
	*storage.SessionStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	retry   bool
}

func (s *publicationWriteStore) RestoreAcceptedFinalForSession(ctx context.Context, expected domain.Session, message, final string) (bool, error) {
	first := false
	s.once.Do(func() { first = true; close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if first && s.retry {
		return false, errors.New("synthetic final save failure")
	}
	return s.SessionStore.RestoreAcceptedFinalForSession(ctx, expected, message, final)
}

type publicationLifecycle struct {
	*app.SessionTurnLifecycle
	entered, release chan struct{}
}

func (l *publicationLifecycle) Finish(ctx context.Context, id domain.SessionID) (domain.Session, bool, error) {
	close(l.entered)
	<-l.release
	return l.SessionTurnLifecycle.Finish(ctx, id)
}

func TestFinalPublicationProducerCancelsBeforePhysicalWriteAndReleasesOnlyVisibleView(t *testing.T) {
	for _, name := range []string{"visible-retry", "menu-during-write", "other-session", "shutdown"} {
		menu := name == "menu-during-write"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			old, base, path, ready := pageSelectionFixture(t)
			if err := old.Close(ctx); err != nil {
				t.Fatal(err)
			}
			store := &publicationWriteStore{SessionStore: base, entered: make(chan struct{}), release: make(chan struct{}), retry: !menu}
			lifecycle, err := app.NewSessionTurnLifecycle(base, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			barrier := &publicationLifecycle{SessionTurnLifecycle: lifecycle, entered: make(chan struct{}), release: make(chan struct{})}
			sessions := []domain.Session{ready}
			if name == "other-session" {
				starting, err := domain.NewStartingSession("22222222-2222-4222-9222-222222222222", "publication-other", "local", domain.ProviderCodex, "/synthetic")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := base.PutStartingIfAbsent(ctx, starting); err != nil {
					t.Fatal(err)
				}
				other, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "publication-other", Generation: 1})
				if err != nil {
					t.Fatal(err)
				}
				if err := base.CompareAndSwap(ctx, starting, other); err != nil {
					t.Fatal(err)
				}
				sessions = append(sessions, other)
			}
			notices := make(chan telegramcontroller.Notification, 16)
			c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, base, retentionSubmitter{}, archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error { notices <- n; return nil }), telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: barrier})
			if err != nil {
				t.Fatal(err)
			}
			var releaseWrite, releaseFinish sync.Once
			defer func() {
				releaseWrite.Do(func() { close(store.release) })
				releaseFinish.Do(func() { close(barrier.release) })
				_ = c.Close(context.Background())
			}()
			if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID()}); err != nil {
				t.Fatal(err)
			}
			f := publicationWire(t, ctx, c, base, ready.ID())
			f.deliver(telegramcontroller.NotificationPromptStatus, "publication-initial")
			oldCarrier := f.wire.last().ID
			prior, stop, visible := c.NativeDeliveryContext(ctx, ready.ID())
			defer stop()
			if !visible {
				t.Fatal("initial card not visible")
			}
			completed := make(chan telegramcontroller.DurableInputProcessReceipt, 1)
			receipt, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "publication-request", Sequence: 1, Payload: []byte("synthetic prompt")}, telegramcontroller.DurableInputCallbacks{
				OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
				OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error {
					completed <- r
					return nil
				},
			})
			if err != nil || !receipt.Accepted {
				t.Fatalf("producer acceptance: %+v %v", receipt, err)
			}
			select {
			case <-store.entered:
			case <-ctx.Done():
				t.Fatal("producer did not reach physical writer")
			}
			select {
			case <-prior.Done():
			case <-time.After(time.Second):
				t.Fatal("routine delivery remained alive at first physical final write")
			}
			_, cleanup, admitted := c.NativeDeliveryContext(ctx, ready.ID())
			cleanup()
			if admitted {
				t.Fatal("routine delivery admitted while final write is active")
			}
			if name == "shutdown" {
				// The first physical attempt has a bounded uncancelled drain;
				// release it with a failure before checking cancellable retry.
				releaseWrite.Do(func() { close(store.release) })
				if err := c.Close(ctx); err != nil {
					t.Fatal(err)
				}
				closedView, cleanup, admitted := c.NativeDeliveryContext(ctx, ready.ID())
				defer cleanup()
				if admitted && closedView.Err() == nil {
					t.Fatal("shutdown renewed final write context")
				}
				return
			}
			var otherView context.Context
			if name == "other-session" {
				if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessions[1].ID()}); err != nil {
					t.Fatal(err)
				}
				otherView, cleanup, admitted = c.NativeDeliveryContext(ctx, sessions[1].ID())
				defer cleanup()
				if !admitted {
					t.Fatal("A's write blocked active B")
				}
			}
			if menu {
				if _, err := c.HandleSemanticMessage(ctx, coordinator.Update{Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "/menu"}); err != nil {
					t.Fatal(err)
				}
			}
			releaseWrite.Do(func() { close(store.release) })
			select {
			case <-barrier.entered:
			case <-ctx.Done():
				t.Fatal("final retry did not reach lifecycle finish")
			}
			_, cleanup, admitted = c.NativeDeliveryContext(ctx, ready.ID())
			cleanup()
			if admitted != (name == "visible-retry") {
				t.Fatalf("write gate release visibility=%t menu=%t", admitted, menu)
			}
			if otherView != nil && otherView.Err() != nil {
				t.Fatal("A's write completion canceled active B")
			}
			reopened, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			history, err := reopened.LoadCardHistory(ctx, ready.ID())
			if err != nil || strings.Count(strings.Join(history, "\n"), "RETENTION_FINAL") != 1 {
				t.Fatalf("physical final not saved exactly once: %v", err)
			}
			state, err := reopened.LoadTelegramUI(ctx)
			if err != nil {
				t.Fatal(err)
			}
			card, _ := state.Card(ready.ID())
			if len(card.PendingFinalOperations) != 1 || card.PendingFinalOperations[0] != "publication-request:final" {
				t.Fatalf("reopen lost exact pending publication: %+v", card.PendingFinalOperations)
			}
			before := len(f.wire.snapshot())
			f.deliver(telegramcontroller.NotificationPromptStatus, "publication-during-finish")
			if len(f.wire.snapshot()) != before {
				t.Fatal("routine refresh published final on previous carrier before Finish")
			}
			restoredSession, err := reopened.Load(ctx, ready.ID())
			if err != nil {
				t.Fatal(err)
			}
			restored := archiveController(t, reopened, nil, telegramcontroller.Options{Recovered: []domain.Session{restoredSession}, UIState: reopened})
			if _, err := restored.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID()}); err != nil {
				t.Fatal(err)
			}
			restartedPrompt := f.prompt
			restartedPrompt.Controller, restartedPrompt.Cards = restored, telegramruntimecomposition.SessionTelegramUIStore{State: reopened}
			routine, err := restartedPrompt.Deliver(ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationPromptStatus, SessionID: ready.ID(), ConversationID: 42}, "publication-reopened-refresh")
			if closeErr := restored.Close(ctx); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil || routine.State != telegramnotify.DeliveryConfirmed || len(f.wire.snapshot()) != before {
				t.Fatalf("reopen leaked pending final: %+v %v", routine, err)
			}
			releaseFinish.Do(func() { close(barrier.release) })
			select {
			case done := <-completed:
				if done.Completion != telegramcontroller.DurableInputSucceeded {
					t.Fatalf("completion=%+v", done)
				}
			case <-ctx.Done():
				t.Fatal("producer completion missing")
			}
			var finalNotice telegramcontroller.Notification
			for len(notices) > 0 {
				n := <-notices
				if n.Kind == telegramcontroller.NotificationFinal {
					if finalNotice.OperationID != "" {
						t.Fatal("producer duplicated final notification")
					}
					finalNotice = n
				}
			}
			if finalNotice.OperationID != "publication-request:final" {
				t.Fatal("exact final notification missing")
			}
			delivered, err := f.completion.Deliver(ctx, finalNotice, finalNotice.OperationID)
			if err != nil || delivered.State != telegramnotify.DeliveryConfirmed {
				t.Fatalf("final delivery: %+v %v", delivered, err)
			}
			packets := f.wire.snapshot()
			finalCard := requireRetireThenNewRichCard(t, packets, before, oldCarrier)
			if finalCard.ID == oldCarrier || name != "other-session" && !strings.Contains(finalCard.Text, "RETENTION_FINAL") {
				t.Fatal("actual producer final did not use a new Rich carrier")
			}
			if name == "other-session" && finalCard.Text != "Фоновая сессия «synthetic» завершена." {
				t.Fatal("A's completion changed existing background notification policy")
			}
			for _, p := range packets[:before] {
				if strings.Contains(p.Text, "RETENTION_FINAL") {
					t.Fatal("old carrier received final content")
				}
			}
			_, cleanup, admitted = c.NativeDeliveryContext(ctx, ready.ID())
			cleanup()
			if menu && admitted {
				t.Fatal("final delivery reopened menu visibility")
			}
			committed, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			state, err = committed.LoadTelegramUI(ctx)
			if err != nil {
				t.Fatal(err)
			}
			card, _ = state.Card(ready.ID())
			if len(card.PendingFinalOperations) != 0 || card.Carrier.MessageID != finalCard.ID {
				t.Fatal("new carrier commit did not clear exact durable final fence")
			}
		})
	}
}

func publicationWire(t *testing.T, ctx context.Context, c *telegramcontroller.Controller, store *storage.SessionStore, id domain.SessionID) *navigationFollowFixture {
	t.Helper()
	if err := store.SetCardCarrier(ctx, id, 42, 91); err != nil {
		t.Fatal(err)
	}
	wire := &navigationFollowHTTP{next: 100}
	client, err := telegram.NewClient("123:publication-synthetic", wire, telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close(context.Background()) })
	codec, err := callbacktoken.New(bytes.Repeat([]byte{29}, 32), nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, time.Now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cards := telegramruntimecomposition.SessionTelegramUIStore{State: store}
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: c}
	_, out, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, UIState: cards, Sender: base, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(time.Now), MessageUI: adapter, Callbacks: adapter, Operations: telegramflow.NewMemoryCallbackOperationStore()})
	if err != nil {
		t.Fatal(err)
	}
	return &navigationFollowFixture{t: t, ctx: ctx, id: id, store: store, wire: wire, out: out, presenter: presenter,
		prompt:     telegrampromptcomposition.Deliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: out},
		completion: telegramcompletioncomposition.CompletionDeliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: out, ConversationID: 42},
	}
}

func TestFinalPublicationVisibilityNeverLendsOldSessionContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222")
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: sessions, UIState: store})
	defer c.Close(context.Background())
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessions[0].ID()}); err != nil {
		t.Fatal(err)
	}
	prior, cleanup, ok := c.NativeDeliveryContext(ctx, sessions[0].ID())
	defer cleanup()
	if !ok {
		t.Fatal("A context missing")
	}
	// The public legacy selection can change active before the ordinary message
	// wrapper records its projection. An existing A context must not serve B.
	if _, err := c.Handle(ctx, coordinator.Update{Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", CallbackQueryID: "publication-switch", SourceMessageID: 91, Text: "sw:" + string(sessions[1].ID())}); err != nil {
		t.Fatal(err)
	}
	_, stop, ok := c.NativeDeliveryContext(ctx, sessions[1].ID())
	stop()
	if ok {
		t.Fatal("B borrowed A's still-live delivery context")
	}
	result, err := c.HandleSemanticMessage(ctx, coordinator.Update{Kind: coordinator.UpdateMessage, ID: 99, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "synthetic ordinary input"})
	if err != nil || result.Card == nil || result.Card.SessionID != sessions[1].ID() {
		t.Fatalf("B ordinary projection: %+v %v", result, err)
	}
	select {
	case <-prior.Done():
	case <-ctx.Done():
		t.Fatal("B ordinary projection retained A context")
	}
	_, stop, ok = c.NativeDeliveryContext(ctx, sessions[1].ID())
	stop()
	if !ok {
		t.Fatal("B did not receive its own delivery context")
	}
}

func TestNativeDeliveryCauseDistinguishesShutdownFromNavigation(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "navigation"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			parent, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111")
			c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: sessions, UIState: store})
			defer c.Close(context.Background())
			if _, err := c.HandleSemanticAction(parent, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessions[0].ID()}); err != nil {
				t.Fatal(err)
			}
			delivery, cleanup, visible := c.NativeDeliveryContext(parent, sessions[0].ID())
			defer cleanup()
			if !visible {
				t.Fatal("visible delivery context missing")
			}
			if shutdown {
				if err := c.Close(parent); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := c.HandleSemanticMessage(parent, coordinator.Update{Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "/menu"}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-delivery.Done():
			case <-parent.Done():
				t.Fatal("delivery cancellation missing")
			}
			if parent.Err() != nil || delivery.Err() != context.Canceled {
				t.Fatal("test must isolate controller cancellation from parent")
			}
			cause := context.Cause(delivery)
			if cause == nil || (cause == context.Canceled) == shutdown {
				t.Fatalf("shutdown=%t cancellation cause=%v", shutdown, cause)
			}
			if shutdown {
				late, cancelLate, visible := c.NativeDeliveryContext(parent, sessions[0].ID())
				defer cancelLate()
				if visible || late.Err() != context.Canceled || context.Cause(late) == context.Canceled {
					t.Fatal("already-closed controller became invisible successful suppression")
				}
			}
		})
	}
}
