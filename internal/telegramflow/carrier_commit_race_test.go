package telegramflow_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type delayedCarrierSender struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	edits   []int64
}

func (s *delayedCarrierSender) SendStatus(context.Context, string, coordinator.Status) (coordinator.Receipt, error) {
	return coordinator.Receipt{MessageID: 5514}, nil
}
func (s *delayedCarrierSender) SendStatusWithKeyboard(ctx context.Context, id string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return s.SendStatus(ctx, id, status)
}
func (s *delayedCarrierSender) EditStatusWithKeyboard(ctx context.Context, operation string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	s.mu.Lock()
	s.edits = append(s.edits, status.SourceMessageID)
	s.mu.Unlock()
	if operation == "old-edit" {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return coordinator.Receipt{}, ctx.Err()
		}
	}
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}

func TestLateOldEditKeepsNewCarrierPresentationAndDurableReceipt(t *testing.T) {
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "reopened-files"
		}
		t.Run(name, func(t *testing.T) { testLateOldEdit(t, disk, false, false) })
		t.Run(name+"/exact-final", func(t *testing.T) { testLateOldEdit(t, disk, true, false) })
		t.Run(name+"/navigation-ABA", func(t *testing.T) { testLateOldEdit(t, disk, false, true) })
	}
}

func testLateOldEdit(t *testing.T, disk, final, aba bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	store := telegramstate.NewMemoryStore()
	var registry telegrampipeline.CallbackRegistry = telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	var operations telegramflow.CallbackOperationStore = telegramflow.NewMemoryCallbackOperationStore()
	callbackPath, operationPath := filepath.Join(t.TempDir(), "callbacks.json"), filepath.Join(t.TempDir(), "operations.json")
	if disk {
		var err error
		registry, err = telegrampipeline.OpenFileCallbackRegistry(callbackPath, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		operations, err = telegramflow.OpenFileCallbackOperationStore(operationPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	oldCard := telegramstate.Card{SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 5512}, Page: telegramstate.Page{Current: 1, Total: 1}, History: []string{"prompt", "answer"}, HistoryKeys: []string{"input", ""}}
	if final {
		oldCard.PendingFinalOperations = []string{"input-A:final", "input-B:final"}
	}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(oldCard)
	}); err != nil {
		t.Fatal(err)
	}
	base := &delayedCarrierSender{started: make(chan struct{}), release: make(chan struct{})}
	defer close(base.release)
	_, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry, UIState: store, Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: operations, Sender: base})
	if err != nil {
		t.Fatal(err)
	}
	input := telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "old edit", Anchors: []string{"old"}}}, View: telegramui.PageView{Page: 1, Pages: 1, Anchor: "old", FollowLatest: true}}
	old, err := telegramflow.PrepareCardRefresh("old-edit", flowSessionID, 42, 5512, input, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initialCard, _ := initial.Card(flowSessionID)
	old.Card.ExpectedCarrierRevision = &initialCard.CarrierRevision
	done := make(chan error, 1)
	go func() {
		_, err := outbound.EnqueuePrepared(ctx, 1, old)
		done <- err
	}()
	select {
	case <-base.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	input.Pages[0] = telegramui.ContentPage{Content: "new voice card", Anchors: []string{"new"}}
	input.View.Anchor = "new"
	newCard, err := telegramflow.PrepareCompletion("new-send", flowSessionID, 42, true, input, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if final {
		newCard.Card.FinalOperationID = "input-A:final"
	}
	if err := outbound.Register(newCard); err != nil {
		t.Fatal(err)
	}
	if receipt, err := outbound.SendStatusWithKeyboard(ctx, newCard.OperationID, newCard.Status, newCard.Keyboard); err != nil || receipt.MessageID != 5514 {
		t.Fatalf("new send=%+v %v", receipt, err)
	}
	wantCarrier := int64(5514)
	if aba {
		// Navigation may legitimately reuse an existing message, without the
		// expected-revision fence of routine background refreshes.
		newCard, err = telegramflow.PrepareCardRefresh("navigation-back", flowSessionID, 42, 5512, input, "", false, nil, presenter)
		if err != nil {
			t.Fatal(err)
		}
		if err := outbound.Register(newCard); err != nil {
			t.Fatal(err)
		}
		if _, err := outbound.EditStatusWithKeyboard(ctx, newCard.OperationID, newCard.Status, newCard.Keyboard); err != nil {
			t.Fatal(err)
		}
		wantCarrier = 5512
	}
	want, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final {
		card, _ := want.Card(flowSessionID)
		if !reflect.DeepEqual(card.PendingFinalOperations, []string{"input-B:final"}) || !reflect.DeepEqual(card.History, oldCard.History) {
			t.Fatalf("final receipt lost history or exact pending custody: %+v", card)
		}
	}
	// Release through a separate channel send so deferred close is safe on fatal.
	base.release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("late edit changed new card: got=%+v want=%+v", got, want)
	}
	if disk {
		registry, err = telegrampipeline.OpenFileCallbackRegistry(callbackPath, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		operations, err = telegramflow.OpenFileCallbackOperationStore(operationPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	durable, found, err := operations.LoadStatus(ctx, old.OperationID)
	if err != nil || !found || durable.Phase != telegramflow.StatusCommitted || durable.Receipt != 5512 {
		t.Errorf("old edit receipt lost: %+v %v", durable, err)
	}
	update := coordinator.Update{ID: 20, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", SourceMessageID: wantCarrier, CallbackQueryID: "new-button", Text: (*newCard.Keyboard)[0][0].CallbackData}
	if _, err := telegrampipeline.AcceptCallback(ctx, update, 7, 42, mustCardStore(t, store), registry, presenter); err != nil {
		t.Errorf("new-card button rejected: %v", err)
	}
	current, _ := got.Card(flowSessionID)
	refresh, err := telegramflow.PrepareCardRefresh("next-refresh", flowSessionID, 42, current.Carrier.MessageID, input, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if refresh.Status.SourceMessageID != wantCarrier {
		t.Fatalf("next refresh targets old carrier %d", refresh.Status.SourceMessageID)
	}
	if err := outbound.Register(refresh); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, refresh.OperationID, refresh.Status, refresh.Keyboard); err != nil {
		t.Fatal(err)
	}
}
