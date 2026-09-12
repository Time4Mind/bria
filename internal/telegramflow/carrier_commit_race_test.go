package telegramflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/carddeliveryguard"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessiondeliverygate"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestPreparedOldEditIsSuppressedAfterNewCarrierCommit(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(telegramstate.Card{SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 5512}, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	base := &delayedCarrierSender{started: make(chan struct{}), release: make(chan struct{})}
	_, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: store, Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base})
	if err != nil {
		t.Fatal(err)
	}
	input := telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "old", Anchors: []string{"old"}}}, View: telegramui.PageView{Page: 1, Pages: 1, Anchor: "old"}}
	old, err := telegramflow.PrepareCardRefresh("stale-edit", flowSessionID, 42, 5512, input, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := store.Load(ctx)
	initialCard, _ := initial.Card(flowSessionID)
	old.Card.ExpectedCarrierRevision = &initialCard.CarrierRevision
	if err := outbound.Register(old); err != nil {
		t.Fatal(err)
	}
	input.Pages[0] = telegramui.ContentPage{Content: "new", Anchors: []string{"new"}}
	input.View.Anchor = "new"
	newCard, err := telegramflow.PrepareCompletion("new-send", flowSessionID, 42, true, "", input, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(newCard); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(ctx, newCard.OperationID, newCard.Status, newCard.Keyboard); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, old.OperationID, old.Status, old.Keyboard); !errors.Is(err, carddeliveryguard.ErrNotCurrent) {
		t.Fatalf("stale edit error = %v, want ErrNotCurrent", err)
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.edits) != 0 {
		t.Fatalf("stale edit reached Telegram: %v", base.edits)
	}
}

func TestDurableSelectedCardIsSuppressedWhenArchiveFallbackChangedActiveSession(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	store := telegramstate.NewMemoryStore()
	fallbackID := domain.SessionID("22222222-2222-4222-9222-222222222222")
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		if err := state.SetCard(telegramstate.Card{SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 5512}, Page: telegramstate.Page{Current: 1, Total: 1}}); err != nil {
			return err
		}
		return state.SetCard(telegramstate.Card{SessionID: fallbackID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 5513}, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	base := &delayedCarrierSender{started: make(chan struct{}), release: make(chan struct{})}
	operations := telegramflow.NewMemoryCallbackOperationStore()
	_, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: store, Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: operations, Sender: base})
	if err != nil {
		t.Fatal(err)
	}
	input := telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "selected", Anchors: []string{"selected"}}}, View: telegramui.PageView{Page: 1, Pages: 1, Anchor: "selected"}}
	prepared, err := telegramflow.PrepareCardRefresh("status:991", flowSessionID, 42, 5512, input, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Card.MakeActive = false
	claimed := telegramflow.CallbackOperation{
		ID: prepared.OperationID, UpdateID: 991, CallbackQueryID: "query-991",
		CallbackDigest: strings.Repeat("a", 64), Phase: telegramflow.CallbackClaimed,
		Plan: telegrampipeline.CallbackPlan{
			OperationID: prepared.OperationID, UpdateID: 991, SessionID: flowSessionID,
			Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 5512},
			Action:  telegramui.ActionSelectSession, Effect: telegrampipeline.EffectSelectSession,
		},
	}
	if err := operations.Create(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	unknown := claimed
	unknown.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := operations.CompareAndSwap(ctx, prepared.OperationID, telegramflow.CallbackClaimed, unknown); err != nil || !changed {
		t.Fatalf("persist unknown callback = %v, %v", changed, err)
	}
	durable := unknown
	durable.Phase, durable.Prepared = telegramflow.CallbackPrepared, &prepared
	if changed, err := operations.CompareAndSwap(ctx, prepared.OperationID, telegramflow.CallbackEffectUnknown, durable); err != nil || !changed {
		t.Fatalf("persist prepared callback = %v, %v", changed, err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = fallbackID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	receipt, handled, err := outbound.DeliverPreparedStatus(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard)
	if err != nil || !handled || receipt.MessageID != prepared.Status.SourceMessageID {
		t.Fatalf("stale durable select settlement = receipt:%#v handled:%t err:%v", receipt, handled, err)
	}
	if len(base.edits) != 0 {
		t.Fatalf("stale durable select reached Telegram: %v", base.edits)
	}
	operation, found, err := operations.Load(ctx, prepared.OperationID)
	if err != nil || !found || operation.Phase != telegramflow.CallbackEffectResolved || operation.Prepared != nil {
		t.Fatalf("stale durable operation = (%#v, %v, %v), want durably resolved without send", operation, found, err)
	}
}

func TestLegacyEmptyActiveSessionDoesNotBlockOrdinaryCardEdit(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		return state.SetCard(telegramstate.Card{
			SessionID: flowSessionID,
			Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 5512},
			Page:      telegramstate.Page{Current: 1, Total: 1},
		})
	}); err != nil {
		t.Fatal(err)
	}
	base := &delayedCarrierSender{started: make(chan struct{}), release: make(chan struct{})}
	close(base.release)
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          store, Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCardRefresh("legacy-empty-active", flowSessionID, 42, 5512,
		telegramui.CardProjectionInput{
			Pages: []telegramui.ContentPage{{Content: "legacy"}},
			View:  telegramui.PageView{Page: 1, Pages: 1},
		}, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatalf("legacy empty ActiveSession blocked ordinary edit: %v", err)
	}
	if len(base.edits) != 1 || base.edits[0] != 5512 {
		t.Fatalf("ordinary edit did not reach its exact carrier: %v", base.edits)
	}
}

func TestSessionDeliveryGateCoversActiveCheckAndPhysicalEdit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	now := time.Unix(1_800_000_000, 0).UTC()
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(telegramstate.Card{SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 5512}, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	gate := &sessiondeliverygate.Gate{}
	base := &delayedCarrierSender{started: make(chan struct{}), release: make(chan struct{}), blockOperation: "gated-edit"}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, now),
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          store, Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base, SessionDeliveryGate: gate,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCardRefresh("gated-edit", flowSessionID, 42, 5512,
		telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "selected"}}, View: telegramui.PageView{Page: 1, Pages: 1}}, "", false, nil, newPresenter(t, now))
	if err != nil {
		t.Fatal(err)
	}
	prepared.Card.MakeActive = true
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	editDone := make(chan error, 1)
	go func() {
		_, editErr := outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard)
		editDone <- editErr
	}()
	waitFlowSignal(t, base.started, "physical edit did not start")
	archiveEntered := make(chan struct{})
	go func() {
		unlock := gate.Lock(flowSessionID)
		close(archiveEntered)
		unlock()
	}()
	select {
	case <-archiveEntered:
		t.Fatal("archive gate entered while physical edit was in progress")
	case <-time.After(30 * time.Millisecond):
	}
	close(base.release)
	if err := <-editDone; err != nil {
		t.Fatal(err)
	}
	waitFlowSignal(t, archiveEntered, "archive gate did not resume after physical edit")
}

func waitFlowSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

type delayedCarrierSender struct {
	started        chan struct{}
	release        chan struct{}
	blockOperation string
	once           sync.Once
	mu             sync.Mutex
	edits          []int64
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
	if operation == "old-edit" || operation == s.blockOperation {
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
	newCard, err := telegramflow.PrepareCompletion("new-send", flowSessionID, 42, true, "", input, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if final {
		newCard.Card.FinalOperationID = "input-A:final"
	}
	if err := outbound.Register(newCard); err != nil {
		t.Fatal(err)
	}
	type sendResult struct {
		receipt coordinator.Receipt
		err     error
	}
	newDone := make(chan sendResult, 1)
	go func() {
		receipt, sendErr := outbound.SendStatusWithKeyboard(ctx, newCard.OperationID, newCard.Status, newCard.Keyboard)
		newDone <- sendResult{receipt: receipt, err: sendErr}
	}()
	select {
	case result := <-newDone:
		t.Fatalf("new carrier bypassed the in-flight old edit: %+v %v", result.receipt, result.err)
	case <-time.After(20 * time.Millisecond):
	}
	// The previous edit must finish before the new carrier can be published;
	// after the new receipt, no old-card mutation may still be in flight.
	base.release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case result := <-newDone:
		if result.err != nil || result.receipt.MessageID != 5514 {
			t.Fatalf("new send=%+v %v", result.receipt, result.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
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
