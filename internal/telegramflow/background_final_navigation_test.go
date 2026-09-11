package telegramflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type backgroundFinalNavigationExecutor struct {
	target domain.SessionID
}

func (executor backgroundFinalNavigationExecutor) HandleCallback(_ context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	projection, err := telegramui.ProjectCardRefresh(telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "background final", Anchors: []string{"final"}, FinalStart: true}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true},
	})
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	return telegramflow.CallbackResult{OperationID: plan.OperationID, Card: &telegramflow.CardOutput{
		SessionID:  executor.target,
		Projection: projection,
		MakeActive: true,
	}}, nil
}

type backgroundFinalNavigationWire struct {
	*sender
	keyboards map[telegramstate.Carrier]int
	retired   []telegramstate.Carrier
	retireErr error
}

func (wire *backgroundFinalNavigationWire) DeactivateInlineKeyboard(_ context.Context, _ string, chatID, messageID int64) error {
	if wire.retireErr != nil {
		return wire.retireErr
	}
	carrier := telegramstate.Carrier{ChatID: chatID, MessageID: messageID}
	wire.keyboards[carrier] = 0
	wire.retired = append(wire.retired, carrier)
	return nil
}

func TestBackgroundFinalKeyboardRetirementFailurePrecedesUnknownCardEditFence(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	state := telegramstate.NewMemoryStore()
	const backgroundID domain.SessionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	previous := telegramstate.Carrier{ChatID: 42, MessageID: 101}
	if err := state.Update(ctx, func(snapshot *telegramstate.State) error {
		const activeID domain.SessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		if err := snapshot.SetCard(telegramstate.Card{
			SessionID: activeID,
			Carrier:   previous,
			Page:      telegramstate.Page{Current: 1, Total: 1},
		}); err != nil {
			return err
		}
		snapshot.ActiveSession = activeID
		return snapshot.SetCard(telegramstate.Card{
			SessionID: backgroundID,
			Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 202},
			Page:      telegramstate.Page{Current: 1, Total: 1},
		})
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCardRefresh("status:901", backgroundID, 42, 202,
		telegramui.CardProjectionInput{
			Pages: []telegramui.ContentPage{{Content: "background final"}},
			View:  telegramui.PageView{Page: 1, Pages: 1},
		}, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Card.MakeActive = true
	prepared.PreviousActiveSessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	prepared.PreviousActiveCarrier = &previous
	claimed := telegramflow.CallbackOperation{
		ID: prepared.OperationID, UpdateID: 901, CallbackQueryID: "query-901",
		CallbackDigest: strings.Repeat("a", 64), Phase: telegramflow.CallbackClaimed,
		Plan: telegrampipeline.CallbackPlan{
			OperationID: prepared.OperationID, UpdateID: 901, SessionID: backgroundID,
			Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 202},
			Action:  telegramui.ActionSelectSession, Effect: telegrampipeline.EffectSelectSession,
		},
	}
	operations := telegramflow.NewMemoryCallbackOperationStore()
	if err := operations.Create(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	unknown := claimed
	unknown.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := operations.CompareAndSwap(ctx, prepared.OperationID, telegramflow.CallbackClaimed, unknown); err != nil || !changed {
		t.Fatalf("persist callback effect fence: changed=%t err=%v", changed, err)
	}
	operation := unknown
	operation.Phase = telegramflow.CallbackPrepared
	operation.Prepared = &prepared
	if changed, err := operations.CompareAndSwap(ctx, prepared.OperationID, telegramflow.CallbackEffectUnknown, operation); err != nil || !changed {
		t.Fatalf("persist prepared callback: changed=%t err=%v", changed, err)
	}
	wire := &backgroundFinalNavigationWire{
		sender:    &sender{receipt: coordinator.Receipt{MessageID: 202}},
		keyboards: map[telegramstate.Carrier]int{previous: 1},
		retireErr: errors.New("retirement unavailable"),
	}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          state, MessageUI: semanticMessageHandler{}, Callbacks: backgroundFinalNavigationExecutor{target: backgroundID},
		Operations: operations, Sender: wire,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err == nil {
		t.Fatal("keyboard retirement failure did not stop the target card edit")
	}
	if wire.sender.edits != 0 {
		t.Fatalf("target card edit ran after failed keyboard retirement: edits=%d", wire.sender.edits)
	}
	stored, found, err := operations.Load(ctx, prepared.OperationID)
	if err != nil || !found || stored.Phase != telegramflow.CallbackPrepared {
		t.Fatalf("failed safe retirement crossed the send-unknown fence: operation=%+v found=%t err=%v", stored, found, err)
	}
}

func TestBackgroundFinalSelectionRetiresPreviousActiveCardBeforeOpeningCompletedSession(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	state := telegramstate.NewMemoryStore()
	const activeID domain.SessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const backgroundID domain.SessionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	activeCarrier := telegramstate.Carrier{ChatID: 42, MessageID: 101}
	if err := state.Update(ctx, func(snapshot *telegramstate.State) error {
		snapshot.ActiveSession = activeID
		return snapshot.SetCard(telegramstate.Card{
			SessionID: activeID,
			Carrier:   activeCarrier,
			Page:      telegramstate.Page{Current: 1, Total: 1, Anchor: "active", FollowLatest: true},
			History:   []string{"active page"},
		})
	}); err != nil {
		t.Fatal(err)
	}
	activePresentation, err := presenter.PresentKeyboardWithManifest(string(activeID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{
		Action: telegramui.ActionOptions,
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(ctx, registry, activeCarrier, activePresentation); err != nil {
		t.Fatal(err)
	}
	wire := &backgroundFinalNavigationWire{
		sender:    &sender{receipt: coordinator.Receipt{MessageID: 202}},
		keyboards: map[telegramstate.Carrier]int{activeCarrier: 1},
	}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: state, MessageUI: semanticMessageHandler{},
		Callbacks:  backgroundFinalNavigationExecutor{target: backgroundID},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: wire,
	})
	if err != nil {
		t.Fatal(err)
	}
	background, err := telegramflow.PrepareCompletion("completion:background", backgroundID, 42, false, telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "background final", Anchors: []string{"final"}, FinalStart: true}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true},
	}, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(background); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(ctx, background.OperationID, background.Status, background.Keyboard); err != nil {
		t.Fatal(err)
	}
	oldRefresh, err := telegramflow.PrepareCardRefresh("old-active-refresh", activeID, 42, 101,
		telegramui.CardProjectionInput{
			Pages: []telegramui.ContentPage{{Content: "active page", Anchors: []string{"active"}}},
			View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "active", FollowLatest: true},
		}, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(oldRefresh); err != nil {
		t.Fatal(err)
	}
	callback := coordinator.Update{
		ID: 801, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: (*background.Keyboard)[0][0].CallbackData, CallbackQueryID: "query-801", SourceMessageID: 202,
	}
	decision, err := handler.Handle(ctx, callback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, "status:801", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	if len(wire.retired) != 1 || wire.retired[0] != activeCarrier || wire.keyboards[activeCarrier] != 0 {
		t.Fatalf("previous active keyboard remains interactive: retired=%v keyboard_buttons=%d", wire.retired, wire.keyboards[activeCarrier])
	}
	got, err := state.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveSession != backgroundID || got.Cards[backgroundID].Carrier.MessageID != 202 {
		t.Fatalf("completed session did not open on notification carrier: active=%s card=%+v", got.ActiveSession, got.Cards[backgroundID])
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, oldRefresh.OperationID, oldRefresh.Status, oldRefresh.Keyboard); err == nil {
		t.Fatal("prepared refresh restored buttons on the retired previous card")
	}
	if wire.sender.edits != 1 || wire.keyboards[activeCarrier] != 0 {
		t.Fatalf("retired previous card was edited again: edits=%d keyboard_buttons=%d", wire.sender.edits, wire.keyboards[activeCarrier])
	}
	oldCallback := coordinator.Update{
		ID: 802, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: activePresentation.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-802", SourceMessageID: 101,
	}
	if _, err := telegrampipeline.AcceptCallback(ctx, oldCallback, 7, 42, mustCardStore(t, state), registry, presenter); err == nil {
		t.Fatal("retired previous card still accepts its old callback")
	}
}

func TestRealControllerMaySelectBackgroundFinalBeforeItsTelegramEdit(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		activeID     domain.SessionID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		backgroundID domain.SessionID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	)
	sessions := make([]domain.Session, 0, 2)
	for index, id := range []domain.SessionID{activeID, backgroundID} {
		starting, err := domain.NewStartingSession(id, domain.IntentID("intent-"+string(id)), "local", domain.ProviderCodex, "/synthetic")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
			t.Fatal(err)
		}
		ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + string(id), Generation: 1})
		if err != nil || store.CompareAndSwap(ctx, starting, ready) != nil {
			t.Fatalf("ready session %d: %v", index, err)
		}
		if err := store.SetCardPrompt(ctx, id, "prompt-"+string(id), "content"); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, ready)
	}
	if err := store.SetCardCarrier(ctx, activeID, 42, 101); err != nil {
		t.Fatal(err)
	}
	controller, err := telegramcontroller.New(7, 42, "local", archiveSwitchUnused{}, store, archiveSwitchUnused{}, archiveSwitchNotify(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Recovered: sessions, UIState: store})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(ctx)
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: activeID}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	wire := &backgroundFinalNavigationWire{sender: &sender{receipt: coordinator.Receipt{MessageID: 202}}, keyboards: map[telegramstate.Carrier]int{{ChatID: 42, MessageID: 101}: 1}}
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: controller}
	cards := telegramruntimecomposition.SessionTelegramUIStore{State: store}
	handler, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: cards, MessageUI: adapter, Callbacks: adapter,
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: wire})
	if err != nil {
		t.Fatal(err)
	}
	background, err := telegramflow.PrepareCompletion("completion:real-background", backgroundID, 42, false,
		telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "background final", Anchors: []string{"final"}, FinalStart: true}}, View: telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true}},
		false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(background); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(ctx, background.OperationID, background.Status, background.Keyboard); err != nil {
		t.Fatal(err)
	}
	update := coordinator.Update{ID: 903, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: (*background.Keyboard)[0][0].CallbackData, CallbackQueryID: "query-903", SourceMessageID: 202}
	decision, err := handler.Handle(ctx, update)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.LoadActiveSession(ctx)
	if err != nil || selected != backgroundID {
		t.Fatalf("real controller did not preselect target: %s, %v", selected, err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, "status:903", decision.Status, decision.Keyboard); err != nil {
		t.Fatalf("normal background-final edit rejected after controller selection: %v", err)
	}
	if len(wire.retired) != 1 || wire.retired[0] != (telegramstate.Carrier{ChatID: 42, MessageID: 101}) {
		t.Fatalf("previous active card was not retired: %v", wire.retired)
	}
}
