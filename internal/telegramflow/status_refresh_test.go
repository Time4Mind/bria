package telegramflow_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type statusRefreshTransport struct {
	edits    int
	status   coordinator.Status
	keyboard *coordinator.KeyboardMarkup
}

type refreshCallbackExecutor struct{}

func (refreshCallbackExecutor) HandleCallback(_ context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	return telegramflow.CallbackResult{OperationID: plan.OperationID, Surface: &telegramflow.SurfaceOutput{
		Text: "cached quota", RichMarkdown: true,
		Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionRefreshStatus}, {Action: telegramui.ActionMenuBack}}}},
	}}, nil
}

func (transport *statusRefreshTransport) SendStatus(context.Context, string, coordinator.Status) (coordinator.Receipt, error) {
	return coordinator.Receipt{MessageID: 99}, nil
}

func (transport *statusRefreshTransport) SendStatusWithKeyboard(ctx context.Context, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return transport.SendStatus(ctx, operationID, status)
}

func (transport *statusRefreshTransport) EditStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	transport.edits++
	transport.status = status
	transport.keyboard = keyboard
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}

func TestStatusRefreshEditsOnlyTheStillCurrentGlobalPresentation(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 99}
	expected, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionRefreshStatus}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(ctx, registry, carrier, expected); err != nil {
		t.Fatal(err)
	}
	transport := &statusRefreshTransport{}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: telegramstate.NewMemoryStore(),
		Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh := telegramflow.SurfaceOutput{Text: "fresh quota", RichMarkdown: true, Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionRefreshStatus}}}}}
	edited, err := outbound.EditCurrentGlobalSurface(ctx, "quota-refresh:1", carrier, expected, fresh)
	if err != nil || !edited || transport.edits != 1 || transport.status.Text != "fresh quota" || transport.status.SourceMessageID != carrier.MessageID {
		t.Fatalf("fresh edit: edited=%t edits=%d status=%+v err=%v", edited, transport.edits, transport.status, err)
	}
	decoded, err := presenter.DecodeCallbackWithMetadata((*transport.keyboard)[0][0].CallbackData)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := registry.Claim(ctx, telegrampipeline.CallbackClaim{
		SessionID: telegramui.GlobalSurfaceID, Carrier: carrier, TokenID: decoded.TokenID,
		ExpiresAt: decoded.ExpiresAt, UpdateID: 10, CallbackQueryID: "fresh",
	})
	if err != nil || claim.Outcome != telegrampipeline.ClaimAccepted {
		t.Fatalf("fresh keyboard claim=%+v err=%v", claim, err)
	}
}

func TestRefreshCommitEventStartsOnlyAfterCachedEditIsBound(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	transport := &statusRefreshTransport{}
	committed := make(chan telegramflow.CallbackCommit, 1)
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: telegramstate.NewMemoryStore(),
		Messages: &messageHandler{}, Callbacks: refreshCallbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: transport,
		OnCallbackCommitted: func(commit telegramflow.CallbackCommit) { committed <- commit },
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := telegramflow.PrepareSurface("initial-status", 42, "", 0, false, telegramflow.SurfaceOutput{
		Text: "old quota", Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionRefreshStatus}}}},
	}, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(initial); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(ctx, initial.OperationID, initial.Status, initial.Keyboard); err != nil {
		t.Fatal(err)
	}
	decision, err := handler.Handle(ctx, coordinator.Update{
		ID: 7, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: (*initial.Keyboard)[0][0].CallbackData, CallbackQueryID: "refresh-7", SourceMessageID: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-committed:
		t.Fatalf("refresh started before cached Telegram edit: %+v", event)
	default:
	}
	if _, err := outbound.EnqueueStatus(ctx, "status:7", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-committed:
		if event.Action != telegramui.ActionRefreshStatus || event.Carrier != (telegramstate.Carrier{ChatID: 42, MessageID: 99}) || event.Presentation.TokenIDs[0] == initial.Presentation.TokenIDs[0] {
			t.Fatalf("commit event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh commit event was not emitted")
	}
}

func TestStatusRefreshDoesNotOverwriteNewerGlobalSurface(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 99}
	expected, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionRefreshStatus}}}})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionMenuBack}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(ctx, registry, carrier, newer); err != nil {
		t.Fatal(err)
	}
	transport := &statusRefreshTransport{}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: telegramstate.NewMemoryStore(),
		Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := outbound.EditCurrentGlobalSurface(ctx, "quota-refresh:2", carrier, expected, telegramflow.SurfaceOutput{
		Text: "late quota", Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionRefreshStatus}}}},
	})
	if err != nil || edited || transport.edits != 0 {
		t.Fatalf("stale edit: edited=%t edits=%d err=%v", edited, transport.edits, err)
	}
}
