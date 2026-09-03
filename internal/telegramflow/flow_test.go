package telegramflow_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecovery"
	"bria/internal/telegramrecoverycomposition"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

const flowSessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")

type messageHandler struct{ calls int }

func (handler *messageHandler) Handle(_ context.Context, update coordinator.Update) (coordinator.Decision, error) {
	handler.calls++
	return coordinator.Decision{Kind: coordinator.DecisionStatus, Status: coordinator.Status{ConversationID: update.ConversationID, Text: "message"}}, nil
}

type semanticMessageHandler struct{}

func (semanticMessageHandler) HandleMessage(_ context.Context, _ coordinator.Update) (telegramflow.MessageResult, error) {
	return telegramflow.MessageResult{Surface: &telegramflow.SurfaceOutput{
		Text: "Меню",
		Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
			{Action: telegramui.ActionMenuSettings}, {Action: telegramui.ActionMenuNew},
		}}},
	}}, nil
}

type globalCallbackExecutor struct {
	plan  telegrampipeline.CallbackPlan
	calls int
}

func (executor *globalCallbackExecutor) HandleCallback(_ context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	executor.plan = plan
	executor.calls++
	return telegramflow.CallbackResult{OperationID: plan.OperationID, Surface: &telegramflow.SurfaceOutput{
		Text: "Настройки",
		Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
			{Action: telegramui.ActionSettingsScreen}, {Action: telegramui.ActionSettingsDetail},
		}}},
	}}, nil
}

func TestFlowSignsTypedGlobalSurfacesAndDurablyExecutesTheirCallbacks(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	operations := telegramflow.NewMemoryCallbackOperationStore()
	base := &sender{receipt: coordinator.Receipt{MessageID: 500}}
	callbacks := &globalCallbackExecutor{}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: telegramstate.NewMemoryStore(),
		MessageUI: semanticMessageHandler{}, Callbacks: callbacks, Operations: operations, Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	menu, err := handler.Handle(context.Background(), coordinator.Update{
		ID: 1, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "/start",
	})
	if err != nil || menu.Keyboard == nil {
		t.Fatalf("typed menu = %#v, %v", menu, err)
	}
	settingsToken := (*menu.Keyboard)[0][0].CallbackData
	if settingsToken == "menu:settings" || len(settingsToken) != callbacktoken.EncodedLength {
		t.Fatalf("menu callback_data = %q", settingsToken)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), "status:1", menu.Status, menu.Keyboard); err != nil {
		t.Fatalf("send typed menu: %v", err)
	}
	settingsUpdate := coordinator.Update{
		ID: 2, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: settingsToken, CallbackQueryID: "query-2", SourceMessageID: 500,
	}
	settings, err := handler.Handle(context.Background(), settingsUpdate)
	if err != nil {
		t.Fatalf("handle signed global callback: %v", err)
	}
	if callbacks.calls != 1 || callbacks.plan.UpdateID != 2 || callbacks.plan.Effect != telegrampipeline.EffectOpenSettings {
		t.Fatalf("global callback plan = %#v calls=%d", callbacks.plan, callbacks.calls)
	}
	if settings.Keyboard == nil || (*settings.Keyboard)[0][0].CallbackData == "settings:screen" {
		t.Fatalf("settings surface is not signed: %#v", settings)
	}
	if _, err := outbound.EditStatusWithKeyboard(context.Background(), "status:2", settings.Status, settings.Keyboard); err != nil {
		t.Fatalf("edit typed settings surface: %v", err)
	}
	op, ok, err := operations.Load(context.Background(), "status:2")
	if err != nil || !ok || op.Phase != telegramflow.CallbackCommitted || op.Receipt != 500 {
		t.Fatalf("global callback operation = %#v, %t, %v", op, ok, err)
	}
}

func TestDurableStatusEnqueueOwnsDeliveryAndExposesExactReceipt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	operations := telegramflow.NewMemoryCallbackOperationStore()
	base := &sender{receipt: coordinator.Receipt{MessageID: 700}}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), MessageUI: semanticMessageHandler{}, Callbacks: &callbackExecutor{},
		Operations: operations, Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := handler.Handle(context.Background(), coordinator.Update{ID: 1, Kind: coordinator.UpdateMessage, ConversationID: 42})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := outbound.EnqueueStatus(context.Background(), "status:1", decision.Status, decision.Keyboard)
	if err != nil || receipt.OperationID != "status:1" || receipt.Sequence != 1 || base.sends != 1 {
		t.Fatalf("durable enqueue = %#v, %v sends=%d", receipt, err, base.sends)
	}
	confirmed, found, err := outbound.ResolveStatusReceipt(context.Background(), "status:1")
	if err != nil || !found || confirmed.MessageID != 700 {
		t.Fatalf("resolved receipt = %#v, %t, %v", confirmed, found, err)
	}
	if _, err := outbound.EnqueueStatus(context.Background(), "status:1", decision.Status, decision.Keyboard); err != nil || base.sends != 1 {
		t.Fatalf("idempotent enqueue error=%v sends=%d", err, base.sends)
	}
}

func TestFlowRejectsRawKeyboardFromLegacyMessageHandler(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	handler, _, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, now),
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          telegramstate.NewMemoryStore(), Messages: rawKeyboardHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{receipt: coordinator.Receipt{MessageID: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(context.Background(), coordinator.Update{ID: 1, Kind: coordinator.UpdateMessage, ConversationID: 42}); err == nil {
		t.Fatal("raw unsigned keyboard was accepted")
	}
}

type rawKeyboardHandler struct{}

func (rawKeyboardHandler) Handle(context.Context, coordinator.Update) (coordinator.Decision, error) {
	keyboard := coordinator.KeyboardMarkup{{{Text: "Меню", CallbackData: "menu:raw"}}}
	return coordinator.Decision{Kind: coordinator.DecisionStatus, Keyboard: &keyboard}, nil
}

type callbackExecutor struct {
	plan  telegrampipeline.CallbackPlan
	calls int
}

type interactionTerminalExecutor struct {
	plan  telegrampipeline.CallbackPlan
	calls int
}

func (executor *interactionTerminalExecutor) HandleCallback(_ context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	executor.calls++
	executor.plan = plan
	return telegramflow.CallbackResult{OperationID: plan.OperationID, Terminal: &telegramflow.TerminalOutput{Text: "Ответ принят"}}, nil
}

func TestInteractionQuestionIsSignedAndTerminalEditInvalidatesCarrierOnlyAfterReceipt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	operations := telegramflow.NewMemoryCallbackOperationStore()
	base := &sender{receipt: coordinator.Receipt{MessageID: 808}}
	executor := &interactionTerminalExecutor{}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), Messages: &messageHandler{}, Callbacks: executor,
		Operations: operations, Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareInteraction(
		"interaction:request-1", flowSessionID, 42, "opaque-request-1",
		telegramflow.SurfaceOutput{Text: "Выберите вариант", Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
			{{Action: telegramui.ActionInteractionChoice, Target: telegramui.ButtonTarget{InteractionChoice: 2}}},
			{{Action: telegramui.ActionInteractionCancel}},
		}}}, presenter,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatal(err)
	}
	choiceToken := (*prepared.Keyboard)[0][0].CallbackData
	cancelToken := (*prepared.Keyboard)[1][0].CallbackData
	decision, err := handler.Handle(context.Background(), coordinator.Update{
		ID: 900, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: choiceToken, CallbackQueryID: "query-900", SourceMessageID: 808,
	})
	if err != nil {
		t.Fatal(err)
	}
	if executor.plan.SessionID != flowSessionID || executor.plan.Interaction == nil ||
		executor.plan.Interaction.RequestID != "opaque-request-1" || executor.plan.Interaction.ChoiceIndex != 2 {
		t.Fatalf("interaction callback plan = %#v", executor.plan)
	}
	if decision.Keyboard == nil || len(*decision.Keyboard) != 0 || decision.Status.SourceMessageID != 808 {
		t.Fatalf("terminal decision = %#v", decision)
	}
	cancelDecoded, err := presenter.DecodeCallbackWithMetadata(cancelToken)
	if err != nil {
		t.Fatal(err)
	}
	claim := telegrampipeline.CallbackClaim{
		SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 808}, TokenID: cancelDecoded.TokenID,
		ExpiresAt: cancelDecoded.ExpiresAt, UpdateID: 901, CallbackQueryID: "query-901",
	}
	before, err := registry.Claim(context.Background(), claim)
	if err != nil || before.Outcome != telegrampipeline.ClaimAccepted {
		t.Fatalf("presentation before terminal receipt = %#v, %v", before, err)
	}
	if _, err := outbound.EditStatusWithKeyboard(context.Background(), "status:900", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	after, err := registry.Claim(context.Background(), claim)
	if err != nil || after.Outcome != telegrampipeline.ClaimStale {
		t.Fatalf("presentation after terminal receipt = %#v, %v", after, err)
	}
	operation, ok, err := operations.Load(context.Background(), "status:900")
	if err != nil || !ok || operation.Phase != telegramflow.CallbackCommitted {
		t.Fatalf("terminal callback operation = %#v, %t, %v", operation, ok, err)
	}
}

func TestOutboundUnknownResolutionBindsExactOriginalOperationAndUpdate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	base := &sender{receipt: coordinator.Receipt{MessageID: 909}}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareOutboundResolution(
		"resolution-prompt:77", 42, "status:original-77", 77,
		telegramflow.SurfaceOutput{Text: "Доставка не подтверждена", Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
			{Action: telegramui.ActionOutboundConfirmDelivered},
			{Action: telegramui.ActionOutboundRetryPossibleDuplicate},
		}}}}, presenter,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatal(err)
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), coordinator.Update{
		ID: 910, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: (*prepared.Keyboard)[0][1].CallbackData, CallbackQueryID: "query-910", SourceMessageID: 909,
	}, 7, 42, nil, registry, presenter)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Effect != telegrampipeline.EffectOutboundRetryPossibleDuplicate || plan.OutboundResolution == nil ||
		plan.OutboundResolution.OperationID != "status:original-77" || plan.OutboundResolution.UpdateID != 77 ||
		plan.OutboundResolution.Decision != telegramui.ActionOutboundRetryPossibleDuplicate {
		t.Fatalf("outbound resolution plan = %#v", plan)
	}
}

func TestUnknownCallbacksAreListedDeterministicallyAndHaveExactSignedRecovery(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	operations := telegramflow.NewMemoryCallbackOperationStore()
	for _, updateID := range []int64{102, 101} {
		operationID := "status:" + strconv.FormatInt(updateID, 10)
		operation := telegramflow.CallbackOperation{
			ID: operationID, UpdateID: updateID, CallbackQueryID: "query-" + strconv.FormatInt(updateID, 10),
			CallbackDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Plan: telegrampipeline.CallbackPlan{
				OperationID: operationID, UpdateID: updateID, SessionID: flowSessionID,
				Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99},
				Action:  telegramui.ActionOptions, Effect: telegrampipeline.EffectToggleOptions,
			},
			Phase: telegramflow.CallbackClaimed,
		}
		if err := operations.Create(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
		unknown := operation
		unknown.Phase = telegramflow.CallbackEffectUnknown
		if changed, err := operations.CompareAndSwap(context.Background(), operationID, telegramflow.CallbackClaimed, unknown); err != nil || !changed {
			t.Fatalf("make operation unknown = %t, %v", changed, err)
		}
	}
	base := &sender{receipt: coordinator.Receipt{MessageID: 1001}}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: operations, Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	unknowns, err := handler.ListUnknown(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 2 || unknowns[0].OperationID != "status:101" || unknowns[1].OperationID != "status:102" ||
		unknowns[0].OwnerUserID != 7 || unknowns[0].OwnerPrivateChatID != 42 || unknowns[0].SessionID != flowSessionID {
		t.Fatalf("unknown callback list = %#v", unknowns)
	}
	text, keyboard, err := telegramrecovery.ProjectUnknown(string(unknowns[0].Phase), string(unknowns[0].SessionID), unknowns[0].OperationID)
	copyUnknown := unknowns[0]
	var prepared telegramflow.Prepared
	if err == nil {
		prepared, err = telegramflow.PrepareSurface("recovery-prompt:101", 42, "", 0, false,
			telegramflow.SurfaceOutput{Text: text, Keyboard: keyboard, Recovery: &copyUnknown}, presenter)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.Status.Text, "Повтор может создать дубль") {
		t.Fatalf("recovery warning = %q", prepared.Status.Text)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatal(err)
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), coordinator.Update{
		ID: 1002, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: (*prepared.Keyboard)[0][1].CallbackData, CallbackQueryID: "query-1002", SourceMessageID: 1001,
	}, 7, 42, nil, registry, presenter)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Effect != telegrampipeline.EffectCallbackEffectRetryPossibleDuplicate || plan.Recovery == nil ||
		plan.Recovery.OperationID != "status:101" || plan.Recovery.UpdateID != 101 || plan.Recovery.SessionID != flowSessionID ||
		plan.Recovery.Carrier != (telegramstate.Carrier{ChatID: 42, MessageID: 99}) || plan.Recovery.Phase != "effect_unknown" {
		t.Fatalf("callback recovery plan = %#v", plan)
	}
}

func TestUnknownCallbackBuildsDurableSignedRecoveryControlWithoutRetry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	operations := telegramflow.NewMemoryCallbackOperationStore()
	callbackText := "signed-token"
	operation := telegramflow.CallbackOperation{
		ID: "status:720", UpdateID: 720, CallbackQueryID: "query-720",
		CallbackDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(callbackText))),
		Plan: telegrampipeline.CallbackPlan{
			OperationID: "status:720", UpdateID: 720, SessionID: flowSessionID,
			Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Action: telegramui.ActionOptions,
			Effect: telegrampipeline.EffectToggleOptions,
		},
		Phase: telegramflow.CallbackClaimed,
	}
	if err := operations.Create(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	unknown := operation
	unknown.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := operations.CompareAndSwap(context.Background(), operation.ID, telegramflow.CallbackClaimed, unknown); err != nil || !changed {
		t.Fatalf("make callback unknown = %t, %v", changed, err)
	}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: operations, Sender: &sender{receipt: coordinator.Receipt{MessageID: 1001}},
	})
	if err != nil {
		t.Fatal(err)
	}
	composer, ok := any(handler).(coordinator.UnknownRecoveryHandler)
	if !ok {
		t.Fatal("Telegram flow does not compose an unknown callback recovery control")
	}
	update := coordinator.Update{ID: 720, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", CallbackQueryID: "query-720", SourceMessageID: 99, Text: callbackText}
	control, decision, err := composer.PrepareUnknownRecovery(context.Background(), update)
	if err != nil {
		t.Fatalf("PrepareUnknownRecovery() error = %v", err)
	}
	if control.OriginalOperationID != operation.ID || control.PromptOperationID != "recovery:callback:720" || control.UpdateID != update.ID ||
		decision.Kind != coordinator.DecisionStatus || decision.Status.ConversationID != 42 || decision.Keyboard == nil {
		t.Fatalf("recovery control/decision = %#v / %#v", control, decision)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), control.PromptOperationID, decision.Status, decision.Keyboard); err != nil {
		t.Fatalf("send signed recovery prompt: %v", err)
	}
	if got, found, err := operations.Load(context.Background(), operation.ID); err != nil || !found || got.Phase != telegramflow.CallbackEffectUnknown {
		t.Fatalf("recovery prompt retried or changed original unknown operation: %#v, %t, %v", got, found, err)
	}
}

func TestExactStatusRecoveryBindingRejectsScopeTamperAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	operations, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, sender, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, time.Now().UTC()), CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(time.Now), UIState: telegramstate.NewMemoryStore(), Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: operations, Sender: &sender{}})
	if err != nil {
		t.Fatal(err)
	}
	status := coordinator.Status{ConversationID: 42, Text: "recover"}
	binding := telegramflow.StatusRecoveryBinding{OperationID: "status:731", UpdateID: 731, Scope: telegramflow.RecoveryScope{Kind: telegramflow.RecoveryScopeSession, SessionID: flowSessionID}, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Sequence: 731, Prepared: false, Edit: false}
	if _, err := sender.EnqueueRecoveryStatus(context.Background(), binding.OperationID, status, nil, binding); err != nil {
		t.Fatalf("EnqueueRecoveryStatus() error = %v", err)
	}
	reopened, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	stored, found, err := reopened.LoadStatus(context.Background(), binding.OperationID)
	if err != nil || !found || stored.Recovery == nil || *stored.Recovery != binding {
		t.Fatalf("reopened recovery binding = %#v, %t, %v", stored, found, err)
	}
	tampered := binding
	tampered.Scope = telegramflow.RecoveryScope{Kind: telegramflow.RecoveryScopeGlobal, SessionID: flowSessionID}
	if _, err := sender.EnqueueRecoveryStatus(context.Background(), binding.OperationID, status, nil, tampered); err == nil {
		t.Fatal("session-to-global scope tamper accepted")
	}
}

func TestPrepareStatusRecoverySignsOnlyExactBoundResolution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	binding := telegramflow.StatusRecoveryBinding{
		OperationID: "status:732", UpdateID: 732,
		Scope:   telegramflow.RecoveryScope{Kind: telegramflow.RecoveryScopeGlobal},
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Sequence: 732, Prepared: true, Edit: true,
	}
	prepared, err := telegramrecoverycomposition.PrepareStatusRecovery(binding.OperationID, 42, binding, newPresenter(t, now))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.OperationID != binding.OperationID || prepared.Surface == nil || prepared.Surface.StatusRecovery == nil ||
		*prepared.Surface.StatusRecovery != binding || prepared.Presentation.StatusRecovery == nil || *prepared.Presentation.StatusRecovery != binding ||
		!prepared.Edit || prepared.Status.SourceMessageID != binding.Carrier.MessageID || prepared.Keyboard == nil || len(*prepared.Keyboard) != 3 {
		t.Fatalf("prepared status recovery = %#v", prepared)
	}
}

func TestPrepareAcceptedTurnRecoveryWarnsAndBindsExactPriorTurn(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	binding := telegrampipeline.AcceptedTurnRecoveryBinding{
		SessionID: flowSessionID, MessageID: "telegram-update:301", BindingGeneration: 7,
	}
	prepared, err := telegramflow.PrepareAcceptedTurnRecovery("turn-recovery:301", 42, binding, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status.ConversationID != 42 || !strings.Contains(prepared.Status.Text, "Повтор может повторно выполнить внешние действия") ||
		prepared.Surface == nil || prepared.Surface.AcceptedTurnRecovery == nil || *prepared.Surface.AcceptedTurnRecovery != binding {
		t.Fatalf("accepted-turn recovery surface = %#v", prepared)
	}
	manifest := prepared.Presentation.AcceptedTurnRecovery
	if manifest == nil || manifest.SessionID != binding.SessionID || manifest.MessageID != binding.MessageID ||
		manifest.BindingGeneration != binding.BindingGeneration {
		t.Fatalf("accepted-turn recovery manifest = %#v", manifest)
	}
	wantLabels := []string{"Считать завершённым/учтённым", "Считать не выполненным и повторить", "Отмена"}
	if prepared.Keyboard == nil || len(*prepared.Keyboard) != len(wantLabels) {
		t.Fatalf("accepted-turn recovery keyboard = %#v", prepared.Keyboard)
	}
	for index, label := range wantLabels {
		if len((*prepared.Keyboard)[index]) != 1 || (*prepared.Keyboard)[index][0].Text != label {
			t.Fatalf("accepted-turn recovery row %d = %#v", index, (*prepared.Keyboard)[index])
		}
	}
}

func TestAcceptedTurnRecoveryCallbackIsOneTimeAndCarriesExactGeneration(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	operations := telegramflow.NewMemoryCallbackOperationStore()
	base := &sender{receipt: coordinator.Receipt{MessageID: 909}}
	executor := &interactionTerminalExecutor{}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), Messages: &messageHandler{}, Callbacks: executor,
		Operations: operations, Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := telegrampipeline.AcceptedTurnRecoveryBinding{
		SessionID: flowSessionID, MessageID: "telegram-update:301", BindingGeneration: 7,
	}
	prepared, err := telegramflow.PrepareAcceptedTurnRecovery("turn-recovery:301", 42, binding, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatal(err)
	}
	callbackUpdate := coordinator.Update{
		ID: 302, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: (*prepared.Keyboard)[1][0].CallbackData, CallbackQueryID: "turn-recovery-302", SourceMessageID: 909,
	}
	decision, err := handler.Handle(context.Background(), callbackUpdate)
	if err != nil {
		t.Fatal(err)
	}
	plan := executor.plan
	if plan.OperationID != "status:302" || plan.Effect != telegrampipeline.EffectAcceptedTurnRetryPossibleDuplicate ||
		plan.AcceptedTurnRecovery == nil || plan.AcceptedTurnRecovery.SessionID != flowSessionID ||
		plan.AcceptedTurnRecovery.MessageID != binding.MessageID || plan.AcceptedTurnRecovery.BindingGeneration != 7 {
		t.Fatalf("accepted-turn callback plan = %#v", plan)
	}
	if _, err := handler.Handle(context.Background(), callbackUpdate); err != nil || executor.calls != 1 {
		t.Fatalf("exact update recovery = %v, calls=%d", err, executor.calls)
	}
	if _, err := outbound.EditStatusWithKeyboard(context.Background(), "status:302", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	replay := callbackUpdate
	replay.ID++
	replay.CallbackQueryID = "turn-recovery-replay"
	replayed, err := handler.Handle(context.Background(), replay)
	if err != nil || replayed.Kind != coordinator.DecisionStatus || replayed.Status.CallbackQueryID != replay.CallbackQueryID || executor.calls != 1 {
		t.Fatalf("second callback = %#v, %v, calls=%d; want one-time stale response", replayed, err, executor.calls)
	}
}

func (executor *callbackExecutor) HandleCallback(_ context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	executor.calls++
	executor.plan = plan
	projection, err := telegramui.ProjectPageNavigation(telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "old", Anchors: []string{"old"}}, {Content: "new", Anchors: []string{"new"}}},
		View:  telegramui.PageView{Page: 2, Pages: 2, Anchor: "new", FollowLatest: true},
	}, telegramui.ActionPagePrevious)
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	return telegramflow.CallbackResult{OperationID: plan.OperationID, Card: &telegramflow.CardOutput{
		SessionID:       flowSessionID,
		Projection:      projection,
		OptionsExpanded: false,
	}}, nil
}

func TestCallbackPreparedBeforeCrashResumesWithoutRepeatingEffectAndCommitsAfterReceipt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := now
	presenter := newPresenterWithClock(t, func() time.Time { return clock })
	root := t.TempDir()
	registryPath := filepath.Join(root, "callback-registry.json")
	operationsPath := filepath.Join(root, "callback-operations.json")
	registry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	operations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	uiStore := telegramstate.NewMemoryStore()
	oldCard := telegramstate.Card{
		SessionID: flowSessionID,
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 99},
		Page:      telegramstate.Page{Current: 2, Total: 2, Anchor: "new", FollowLatest: true},
		History:   []string{"old", "new"},
	}
	if err := uiStore.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(oldCard)
	}); err != nil {
		t.Fatal(err)
	}
	presentation, err := presenter.PresentKeyboardWithManifest(string(flowSessionID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 1}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, oldCard.Carrier, presentation); err != nil {
		t.Fatal(err)
	}
	callbackUpdate := coordinator.Update{
		ID: 501, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: presentation.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-501", SourceMessageID: 99,
	}
	firstExecutor := &callbackExecutor{}
	firstHandler, _, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: firstExecutor,
		Operations: operations, Sender: &sender{receipt: coordinator.Receipt{MessageID: 99}},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDecision, err := firstHandler.Handle(context.Background(), callbackUpdate)
	if err != nil || firstExecutor.calls != 1 {
		t.Fatalf("first callback = %#v, err=%v effect calls=%d", firstDecision, err, firstExecutor.calls)
	}
	sameProcessDuplicate, err := firstHandler.Handle(context.Background(), callbackUpdate)
	if err != nil || sameProcessDuplicate.Status.Text != firstDecision.Status.Text || firstExecutor.calls != 1 {
		t.Fatalf("same-process duplicate = %#v, err=%v effect calls=%d", sameProcessDuplicate, err, firstExecutor.calls)
	}

	// Simulate a restart after the semantic effect was stored but before the
	// Telegram carrier call began.
	clock = now.Add(2 * time.Minute)
	restartedPresenter := newPresenterWithClock(t, func() time.Time { return clock })
	reopenedRegistry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	reopenedOperations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	secondExecutor := &callbackExecutor{}
	base := &sender{receipt: coordinator.Receipt{MessageID: 99}}
	secondHandler, secondSender, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: restartedPresenter, CallbackRegistry: reopenedRegistry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: secondExecutor,
		Operations: reopenedOperations, Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumedDecision, err := secondHandler.Handle(context.Background(), callbackUpdate)
	if err != nil {
		t.Fatalf("resume prepared callback: %v", err)
	}
	if secondExecutor.calls != 0 || resumedDecision.Status.Text != firstDecision.Status.Text {
		t.Fatalf("resumed effect calls=%d decision=%#v want stored %#v", secondExecutor.calls, resumedDecision, firstDecision)
	}
	resumedToken := (*resumedDecision.Keyboard)[0][0].CallbackData
	if resumedToken == (*firstDecision.Keyboard)[0][0].CallbackData {
		t.Fatal("restart reused an expired callback token")
	}
	if _, err := restartedPresenter.DecodeCallback(resumedToken); err != nil {
		t.Fatalf("restart callback token is not currently valid: %v", err)
	}
	if _, err := secondSender.EditStatusWithKeyboard(context.Background(), "status:501", resumedDecision.Status, resumedDecision.Keyboard); err != nil {
		t.Fatalf("send resumed prepared callback: %v", err)
	}
	if base.edits != 1 {
		t.Fatalf("carrier edits=%d want 1", base.edits)
	}
	committed, ok, err := reopenedOperations.Load(context.Background(), "status:501")
	if err != nil || !ok || committed.Phase != telegramflow.CallbackCommitted || committed.Receipt != 99 {
		t.Fatalf("committed operation = %#v, %t, %v", committed, ok, err)
	}
	duplicate, err := secondHandler.Handle(context.Background(), callbackUpdate)
	if err != nil || duplicate.Kind != coordinator.DecisionSkip || secondExecutor.calls != 0 {
		t.Fatalf("committed duplicate = %#v, err=%v effect calls=%d", duplicate, err, secondExecutor.calls)
	}
}

func TestCallbackUnknownSendIsDurableAndNeverAutomaticallyRepeated(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	root := t.TempDir()
	registry, err := telegrampipeline.OpenFileCallbackRegistry(filepath.Join(root, "registry.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	operationsPath := filepath.Join(root, "operations.json")
	operations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	uiStore := telegramstate.NewMemoryStore()
	oldCard := telegramstate.Card{SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Page: telegramstate.Page{Current: 2, Total: 2, Anchor: "new", FollowLatest: true}, History: []string{"old", "new"}}
	if err := uiStore.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(oldCard)
	}); err != nil {
		t.Fatal(err)
	}
	presentation, err := presenter.PresentKeyboardWithManifest(string(flowSessionID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, oldCard.Carrier, presentation); err != nil {
		t.Fatal(err)
	}
	update := coordinator.Update{ID: 601, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: presentation.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-601", SourceMessageID: 99}
	executor := &callbackExecutor{}
	ambiguousBase := &sender{err: errors.New("timeout after request write")}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: executor, Operations: operations, Sender: ambiguousBase,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := handler.Handle(context.Background(), update)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(context.Background(), "status:601", decision.Status, decision.Keyboard); err == nil {
		t.Fatal("ambiguous carrier result returned no error")
	}

	reopened, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	safeBase := &sender{receipt: coordinator.Receipt{MessageID: 99}}
	secondExecutor := &callbackExecutor{}
	secondHandler, secondSender, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: secondExecutor, Operations: reopened, Sender: safeBase,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondHandler.Handle(context.Background(), update); !errors.Is(err, telegrampipeline.ErrUnknownOperation) {
		t.Fatalf("restart callback error=%v want unknown operation", err)
	}
	if _, err := secondSender.EditStatusWithKeyboard(context.Background(), "status:601", decision.Status, decision.Keyboard); !errors.Is(err, telegrampipeline.ErrUnknownOperation) {
		t.Fatalf("direct durable resend error=%v want unknown operation", err)
	}
	if secondExecutor.calls != 0 || safeBase.edits != 0 {
		t.Fatalf("automatic replay occurred: effects=%d carrier edits=%d", secondExecutor.calls, safeBase.edits)
	}
	if err := secondSender.ConfirmUnknownSend(context.Background(), "status:601", coordinator.Receipt{MessageID: 100}); err == nil {
		t.Fatal("unknown callback edit accepted a receipt for a different carrier")
	}
	if err := secondSender.RetryUnknownSend(context.Background(), "status:601"); err != nil {
		t.Fatalf("explicit verified-not-delivered resolution: %v", err)
	}
	if _, err := secondSender.EditStatusWithKeyboard(context.Background(), "status:601", decision.Status, decision.Keyboard); err != nil {
		t.Fatalf("explicitly authorized carrier retry: %v", err)
	}
	if safeBase.edits != 1 {
		t.Fatalf("explicit retry carrier edits=%d want 1", safeBase.edits)
	}
}

func TestConfirmUnknownStatusRejectsReceiptForDifferentEditCarrier(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	operations := telegramflow.NewMemoryCallbackOperationStore()
	status := telegramflow.StatusOperation{
		ID: "status:602", Sequence: 602,
		Status: coordinator.Status{ConversationID: 42, Text: "edit", SourceMessageID: 99},
		Edit:   true, Phase: telegramflow.StatusQueued,
	}
	if _, _, err := operations.EnqueueStatus(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	unknown := status
	unknown.Phase = telegramflow.StatusSendUnknown
	if changed, err := operations.CompareAndSwapStatus(context.Background(), status.ID, telegramflow.StatusQueued, unknown); err != nil || !changed {
		t.Fatalf("make status unknown = %t, %v", changed, err)
	}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, now),
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          telegramstate.NewMemoryStore(), Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: operations, Sender: &sender{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.ConfirmUnknownStatus(context.Background(), status.ID, coordinator.Receipt{MessageID: 100}); err == nil {
		t.Fatal("unknown status edit accepted a receipt for a different carrier")
	}
	got, found, err := operations.LoadStatus(context.Background(), status.ID)
	if err != nil || !found || got.Phase != telegramflow.StatusSendUnknown {
		t.Fatalf("wrong receipt changed status operation = %#v, %t, %v", got, found, err)
	}
}

type sender struct {
	receipt coordinator.Receipt
	err     error
	sends   int
	edits   int
}

type failNextUpdateStore struct {
	inner    telegramstate.Store
	failNext bool
}

func (store *failNextUpdateStore) Load(ctx context.Context) (telegramstate.State, error) {
	return store.inner.Load(ctx)
}

func (store *failNextUpdateStore) Update(ctx context.Context, change func(*telegramstate.State) error) error {
	if store.failNext {
		store.failNext = false
		return errors.New("local UI persistence unavailable")
	}
	return store.inner.Update(ctx, change)
}

func (sender *sender) SendStatus(context.Context, string, coordinator.Status) (coordinator.Receipt, error) {
	sender.sends++
	return sender.receipt, sender.err
}
func (sender *sender) SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	sender.sends++
	return sender.receipt, sender.err
}
func (sender *sender) EditStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	sender.edits++
	return sender.receipt, sender.err
}

func TestFlowAuthenticatesPlansAndBindsReplacementOnlyAfterConfirmedEdit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	uiStore := telegramstate.NewMemoryStore()
	oldCard := telegramstate.Card{
		SessionID: flowSessionID,
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 99},
		Page:      telegramstate.Page{Current: 2, Total: 2, Anchor: "new", FollowLatest: true},
		History:   []string{"old", "new"},
	}
	if err := uiStore.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(oldCard)
	}); err != nil {
		t.Fatal(err)
	}
	oldPresentation, err := presenter.PresentKeyboardWithManifest(string(flowSessionID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 1}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, oldCard.Carrier, oldPresentation); err != nil {
		t.Fatal(err)
	}
	base := &sender{receipt: coordinator.Receipt{MessageID: 99}}
	executor := &callbackExecutor{}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID:        7,
		OwnerPrivateChatID: 42,
		Presenter:          presenter,
		CallbackRegistry:   registry,
		UIState:            uiStore,
		Messages:           &messageHandler{},
		Callbacks:          executor,
		Operations:         telegramflow.NewMemoryCallbackOperationStore(),
		Sender:             base,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := handler.Handle(context.Background(), coordinator.Update{
		ID: 10, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: oldPresentation.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query", SourceMessageID: 99,
	})
	if err != nil {
		t.Fatalf("Handle(callback) error = %v", err)
	}
	if executor.plan.Effect != telegrampipeline.EffectProjectPage || executor.plan.Action != telegramui.ActionPagePrevious {
		t.Fatalf("semantic plan = %#v", executor.plan)
	}
	if decision.Kind != coordinator.DecisionStatus || decision.Keyboard == nil || decision.Status.SourceMessageID != 99 || decision.Status.Text != "old" {
		t.Fatalf("decision = %#v", decision)
	}
	newToken := (*decision.Keyboard)[0][0].CallbackData
	if _, err := telegrampipeline.AcceptCallback(context.Background(), coordinator.Update{
		ID: 11, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: newToken, CallbackQueryID: "too-early", SourceMessageID: 99,
	}, 7, 42, mustCardStore(t, uiStore), registry, presenter); !errors.Is(err, telegrampipeline.ErrStaleCallback) {
		t.Fatalf("unconfirmed presentation error = %v, want stale", err)
	}

	if _, err := outbound.EditStatusWithKeyboard(context.Background(), "status:10", decision.Status, decision.Keyboard); err != nil {
		t.Fatalf("EditStatusWithKeyboard() error = %v", err)
	}
	if base.edits != 1 {
		t.Fatalf("base edits = %d, want 1", base.edits)
	}
	stored, err := uiStore.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	card, ok := stored.Card(flowSessionID)
	if !ok || card.Page.Current != 1 || card.Carrier.MessageID != 99 || card.History[0] != "old" {
		t.Fatalf("committed card = %#v", card)
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), coordinator.Update{
		ID: 12, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: newToken, CallbackQueryID: "query-2", SourceMessageID: 99,
	}, 7, 42, mustCardStore(t, uiStore), registry, presenter)
	if err != nil || accepted.Action != telegramui.ActionPagePrevious {
		t.Fatalf("replacement callback = %#v, %v", accepted, err)
	}
}

func TestFlowDoesNotBindOrCommitWhenCarrierWriteHasNoReceipt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	uiStore := telegramstate.NewMemoryStore()
	base := &sender{err: errors.New("timeout")}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCompletion("completion:1", flowSessionID, 42, true, telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "final", Anchors: []string{"final"}}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true},
	}, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), prepared.OperationID, prepared.Status, prepared.Keyboard); err == nil {
		t.Fatal("SendStatusWithKeyboard() error = nil")
	}
	state, err := uiStore.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Card(flowSessionID); ok {
		t.Fatal("failed carrier write committed UI card")
	}
}

func TestPrepareCompletionSeparatesActiveFinalAndBackgroundNotification(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	input := telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "before", Anchors: []string{"before"}}, {Content: "SECRET FINAL", Anchors: []string{"final"}}},
		View:  telegramui.PageView{Page: 1, Pages: 2, Anchor: "before"},
	}
	active, err := telegramflow.PrepareCompletion("completion:active", flowSessionID, 42, true, input, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status.Text != "SECRET FINAL" || active.Status.SourceMessageID != 0 || active.Keyboard == nil {
		t.Fatalf("active completion = %#v", active)
	}
	background, err := telegramflow.PrepareCompletion("completion:background", flowSessionID, 42, false, input, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if background.Status.Text != "Фоновая сессия завершена." || background.Status.SourceMessageID != 0 || background.Keyboard == nil {
		t.Fatalf("background completion = %#v", background)
	}
	if background.Status.Text == "SECRET FINAL" || len(*background.Keyboard) != 1 || len((*background.Keyboard)[0]) != 1 {
		t.Fatalf("background completion leaked final or duplicated controls: %#v", background)
	}
	if background.Card.Projection.Notification == nil || background.Card.Projection.Card.Pages[1].Content != "SECRET FINAL" {
		t.Fatalf("background completion did not retain final for later card: %#v", background.Card)
	}
}

func TestBackgroundCompletionBindsOnlyConfirmedNotificationCarrierAndKeepsActiveSession(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	uiStore := telegramstate.NewMemoryStore()
	activeSessionID := domain.SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err := uiStore.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = activeSessionID
		return state.SetCard(telegramstate.Card{
			SessionID: activeSessionID,
			Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 101},
			Page:      telegramstate.Page{Current: 1, Total: 1, Anchor: "active", FollowLatest: true},
			History:   []string{"active"},
		})
	}); err != nil {
		t.Fatal(err)
	}
	base := &sender{receipt: coordinator.Receipt{MessageID: 202}}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCompletion("completion:background:1", flowSessionID, 42, false, telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "SECRET FINAL", Anchors: []string{"final"}}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true},
	}, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	callbackData := (*prepared.Keyboard)[0][0].CallbackData
	callbackUpdate := coordinator.Update{ID: 801, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: callbackData, CallbackQueryID: "query-801", SourceMessageID: 202}
	if _, err := telegrampipeline.AcceptCallback(context.Background(), callbackUpdate, 7, 42, mustCardStore(t, uiStore), registry, presenter); !errors.Is(err, telegrampipeline.ErrStaleCallback) {
		t.Fatalf("unconfirmed background notification callback error=%v want stale", err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(context.Background(), prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatalf("send background completion: %v", err)
	}
	state, err := uiStore.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backgroundCard, ok := state.Card(flowSessionID)
	if !ok || backgroundCard.Carrier.MessageID != 202 || backgroundCard.History[0] != "SECRET FINAL" {
		t.Fatalf("background completion card = %#v", backgroundCard)
	}
	if state.ActiveSession != activeSessionID {
		t.Fatalf("active session changed to %q", state.ActiveSession)
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), callbackUpdate, 7, 42, mustCardStore(t, uiStore), registry, presenter)
	if err != nil || accepted.SessionID != flowSessionID || accepted.Action != telegramui.ActionSelectSession {
		t.Fatalf("confirmed background callback = %#v, %v", accepted, err)
	}
}

func TestHandlerDoesNotRevealUIToForeignCallbackAndKeepsOwnerInvalidButtonRecoverable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	messages := &messageHandler{}
	handler, _, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          telegramstate.NewMemoryStore(), Messages: messages, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(),
		Sender:     &sender{receipt: coordinator.Receipt{MessageID: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := handler.Handle(context.Background(), coordinator.Update{
		ID: 1, Kind: coordinator.UpdateCallback, ActorID: 8, ConversationID: 43, ConversationKind: "private",
		Text: "not-a-token", CallbackQueryID: "foreign", SourceMessageID: 1,
	})
	if err != nil || foreign.Kind != coordinator.DecisionSkip || messages.calls != 0 {
		t.Fatalf("foreign callback decision = %#v, err=%v, message calls=%d", foreign, err, messages.calls)
	}
	owner, err := handler.Handle(context.Background(), coordinator.Update{
		ID: 2, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: "not-a-token", CallbackQueryID: "owner", SourceMessageID: 1,
	})
	if err != nil {
		t.Fatalf("owner invalid callback stopped handler: %v", err)
	}
	if owner.Kind != coordinator.DecisionStatus || owner.Status.SourceMessageID != 0 || owner.Status.CallbackQueryID != "owner" || owner.Status.Text == "" {
		t.Fatalf("owner invalid callback decision = %#v", owner)
	}
}

func TestConfirmedCallbackReceiptFinishesLocalCommitAfterRestartWithoutResend(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	root := t.TempDir()
	registry, err := telegrampipeline.OpenFileCallbackRegistry(filepath.Join(root, "registry.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	operationsPath := filepath.Join(root, "operations.json")
	operations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	baseUI := telegramstate.NewMemoryStore()
	uiStore := &failNextUpdateStore{inner: baseUI}
	oldCard := telegramstate.Card{SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Page: telegramstate.Page{Current: 2, Total: 2, Anchor: "new", FollowLatest: true}, History: []string{"old", "new"}}
	if err := uiStore.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = flowSessionID
		return state.SetCard(oldCard)
	}); err != nil {
		t.Fatal(err)
	}
	presentation, err := presenter.PresentKeyboardWithManifest(string(flowSessionID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, oldCard.Carrier, presentation); err != nil {
		t.Fatal(err)
	}
	update := coordinator.Update{ID: 701, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: presentation.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-701", SourceMessageID: 99}
	firstExecutor := &callbackExecutor{}
	base := &sender{receipt: coordinator.Receipt{MessageID: 99}}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: firstExecutor, Operations: operations, Sender: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := handler.Handle(context.Background(), update)
	if err != nil {
		t.Fatal(err)
	}
	uiStore.failNext = true
	if _, err := outbound.EditStatusWithKeyboard(context.Background(), "status:701", decision.Status, decision.Keyboard); err == nil {
		t.Fatal("post-receipt local commit failure returned no error")
	}
	confirmed, ok, err := operations.Load(context.Background(), "status:701")
	if err != nil || !ok || confirmed.Phase != telegramflow.CallbackReceiptConfirmed || confirmed.Receipt != 99 {
		t.Fatalf("post-receipt state = %#v, %t, %v", confirmed, ok, err)
	}

	reopenedOperations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	secondExecutor := &callbackExecutor{}
	safeBase := &sender{receipt: coordinator.Receipt{MessageID: 99}}
	secondHandler, _, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: uiStore, Messages: &messageHandler{}, Callbacks: secondExecutor, Operations: reopenedOperations, Sender: safeBase,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := secondHandler.Handle(context.Background(), update)
	if err != nil || result.Kind != coordinator.DecisionSkip {
		t.Fatalf("receipt recovery = %#v, %v", result, err)
	}
	if secondExecutor.calls != 0 || safeBase.edits != 0 {
		t.Fatalf("receipt recovery repeated side effect: executor=%d carrier=%d", secondExecutor.calls, safeBase.edits)
	}
	committed, ok, err := reopenedOperations.Load(context.Background(), "status:701")
	if err != nil || !ok || committed.Phase != telegramflow.CallbackCommitted {
		t.Fatalf("recovered committed state = %#v, %t, %v", committed, ok, err)
	}
}

func newPresenter(t *testing.T, now time.Time) *telegrambridge.Presenter {
	return newPresenterWithClock(t, func() time.Time { return now })
}

func newPresenterWithClock(t *testing.T, now func() time.Time) *telegrambridge.Presenter {
	t.Helper()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 4096)), now)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return presenter
}

func mustCardStore(t *testing.T, store telegramstate.Store) telegrampipeline.StateCardStore {
	t.Helper()
	cards, err := telegrampipeline.NewStateCardStore(store)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}
