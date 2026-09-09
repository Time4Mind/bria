package telegramflow_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestConfirmedCallbackReplayAfterRestartPreservesNewCarrier(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	root := t.TempDir()
	openStores := func() (*telegramstate.FileStore, *telegrampipeline.FileCallbackRegistry, *telegramflow.FileCallbackOperationStore) {
		t.Helper()
		ui, err := telegramstate.OpenFileStore(filepath.Join(root, "ui.json"))
		if err != nil {
			t.Fatal(err)
		}
		registry, err := telegrampipeline.OpenFileCallbackRegistry(filepath.Join(root, "registry.json"), func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		operations, err := telegramflow.OpenFileCallbackOperationStore(filepath.Join(root, "operations.json"))
		if err != nil {
			t.Fatal(err)
		}
		return ui, registry, operations
	}
	ui, registry, operations := openStores()
	oldCard := telegramstate.Card{
		SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99},
		Page:    telegramstate.Page{Current: 2, Total: 2, Anchor: "new", FollowLatest: true},
		History: []string{"old", "new", "latest answer"}, PendingFinalOperations: []string{"next-input:final"},
	}
	if err := ui.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(oldCard)
	}); err != nil {
		t.Fatal(err)
	}
	presentation, err := presenter.PresentKeyboardWithManifest(string(flowSessionID), nil, telegramui.CardKeyboard{
		Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(ctx, registry, oldCard.Carrier, presentation); err != nil {
		t.Fatal(err)
	}
	update := coordinator.Update{
		ID: 701, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: presentation.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-701", SourceMessageID: 99,
	}
	failingUI := &failNextUpdateStore{inner: ui}
	base := &sender{receipt: coordinator.Receipt{MessageID: 99}}
	config := telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: failingUI, Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: operations, Sender: base,
	}
	handler, outbound, err := telegramflow.New(config)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := handler.Handle(ctx, update)
	if err != nil {
		t.Fatal(err)
	}
	failingUI.failNext = true
	if _, err := outbound.EditStatusWithKeyboard(ctx, "status:701", decision.Status, decision.Keyboard); err == nil {
		t.Fatal("post-receipt UI failure returned no error")
	}
	confirmed, found, err := operations.Load(ctx, "status:701")
	if err != nil || !found || confirmed.Phase != telegramflow.CallbackReceiptConfirmed || confirmed.Receipt != 99 {
		t.Fatalf("old receipt was not durable: %+v found=%t err=%v", confirmed, found, err)
	}

	// A subsequent publication owns carrier 100 before the old receipt retries.
	input := telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{
			{Content: "old", Anchors: []string{"old"}}, {Content: "new", Anchors: []string{"new"}},
			{Content: "latest answer", Anchors: []string{"latest"}},
		},
		View: telegramui.PageView{Page: 3, Pages: 3, Anchor: "latest", FollowLatest: true},
	}
	newCard, err := telegramflow.PrepareCompletion("new-send", flowSessionID, 42, true, input, true, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(newCard); err != nil {
		t.Fatal(err)
	}
	base.receipt.MessageID = 100
	if receipt, err := outbound.SendStatusWithKeyboard(ctx, newCard.OperationID, newCard.Status, newCard.Keyboard); err != nil || receipt.MessageID != 100 {
		t.Fatalf("new carrier not confirmed: %+v %v", receipt, err)
	}
	if base.edits != 1 || base.sends != 1 {
		t.Fatalf("initial wire deliveries: edits=%d sends=%d", base.edits, base.sends)
	}

	// Reopen all three stores, including the physical UI document.
	ui, registry, operations = openStores()
	want, err := ui.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := want.Card(flowSessionID)
	if current.Carrier.MessageID != 100 || current.Page.Total != 3 || !reflect.DeepEqual(current.History, oldCard.History) ||
		!reflect.DeepEqual(current.PendingFinalOperations, oldCard.PendingFinalOperations) {
		t.Fatalf("new carrier/page/history/pending did not survive reopen: %+v", current)
	}
	secondExecutor, safeBase := &callbackExecutor{}, &sender{receipt: coordinator.Receipt{MessageID: 99}}
	config.UIState, config.CallbackRegistry, config.Operations = ui, registry, operations
	config.Callbacks, config.Sender = secondExecutor, safeBase
	secondHandler, _, err := telegramflow.New(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := secondHandler.Handle(ctx, update)
	if err != nil || result.Kind != coordinator.DecisionSkip {
		t.Fatalf("old receipt replay: %+v %v", result, err)
	}
	if secondExecutor.calls != 0 || safeBase.edits != 0 || safeBase.sends != 0 {
		t.Errorf("receipt replay repeated side effects: executor=%d edits=%d sends=%d", secondExecutor.calls, safeBase.edits, safeBase.sends)
	}
	ui, registry, operations = openStores()
	committed, found, err := operations.Load(ctx, "status:701")
	if err != nil || !found || committed.Phase != telegramflow.CallbackCommitted || committed.Receipt != 99 {
		t.Errorf("old receipt was not settled exactly: %+v found=%t err=%v", committed, found, err)
	}
	got, err := ui.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		gotCard, _ := got.Card(flowSessionID)
		t.Errorf("old callback receipt overwrote new carrier/page/history/pending: got=%+v want=%+v", gotCard, current)
	}
	button := coordinator.Update{
		ID: 702, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		SourceMessageID: 100, CallbackQueryID: "new-carrier-button", Text: (*newCard.Keyboard)[0][0].CallbackData,
	}
	if _, err := telegrampipeline.AcceptCallback(ctx, button, 7, 42, mustCardStore(t, ui), registry, presenter); err != nil {
		t.Errorf("new carrier buttons lost after old receipt replay: %v", err)
	}
}
