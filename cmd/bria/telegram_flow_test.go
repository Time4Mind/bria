package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramui"
)

type semanticControllerStub struct {
	messageResult telegramcontroller.SemanticActionResult
	actionResult  telegramcontroller.SemanticActionResult
	message       coordinator.Update
	action        telegramcontroller.SemanticAction
}

type statusDeliveryStub struct {
	swept      chan struct{}
	afterSweep func()
}

func (stub *statusDeliveryStub) DeliverPendingStatuses(context.Context, int) error {
	if stub.afterSweep != nil {
		stub.afterSweep()
	}
	select {
	case stub.swept <- struct{}{}:
	default:
	}
	return nil
}

type checkpointLoadStub struct {
	stored coordinator.StoredCheckpoint
	found  bool
}

func (stub checkpointLoadStub) Load(context.Context) (coordinator.StoredCheckpoint, bool, error) {
	return stub.stored, stub.found, nil
}

func (checkpointLoadStub) Save(context.Context, uint64, coordinator.Checkpoint) (coordinator.StoredCheckpoint, error) {
	return coordinator.StoredCheckpoint{}, errors.New("unexpected save")
}

type orderingConfirmerStub struct {
	delivered *bool
	called    chan struct{}
}

func (stub *orderingConfirmerStub) ConfirmEnqueuedOutbound(context.Context, string, int64) (coordinator.StoredCheckpoint, error) {
	if !*stub.delivered {
		return coordinator.StoredCheckpoint{}, errors.New("confirmed before durable delivery")
	}
	select {
	case stub.called <- struct{}{}:
	default:
	}
	return coordinator.StoredCheckpoint{}, nil
}

func (stub *semanticControllerStub) HandleSemanticMessage(_ context.Context, update coordinator.Update) (telegramcontroller.SemanticActionResult, error) {
	stub.message = update
	return stub.messageResult, nil
}

func (stub *semanticControllerStub) HandleSemanticAction(_ context.Context, action telegramcontroller.SemanticAction) (telegramcontroller.SemanticActionResult, error) {
	stub.action = action
	return stub.actionResult, nil
}

func TestTelegramControllerFlowAdapterMapsSignedCallbackAndPreservesCardHeader(t *testing.T) {
	const sessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	stub := &semanticControllerStub{actionResult: telegramcontroller.SemanticActionResult{Card: &telegramcontroller.SemanticCard{
		SessionID: sessionID,
		Effect:    telegramcontroller.SemanticEditSameCarrier,
		Header:    "typed header\n\n",
		Pages:     []telegramcontroller.SemanticContentPage{{Content: "page", Anchors: []string{"history:1"}}},
		View:      telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "history:1", FollowLatest: true},
		Working:   true, SelectableSessionIDs: []domain.SessionID{sessionID}, SessionRowSizes: []int{1},
	}}}
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: stub}

	result, err := adapter.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
		OperationID: "status:41", UpdateID: 41, SessionID: sessionID,
		Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{FollowLatest: true},
		Effect: telegrampipeline.EffectProjectPage,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantAction := telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticPageLatest, SessionID: sessionID,
		FollowLatest: true, UpdateID: 41,
	}
	if !reflect.DeepEqual(stub.action, wantAction) {
		t.Fatalf("semantic action = %#v, want %#v", stub.action, wantAction)
	}
	if result.OperationID != "status:41" || result.Card == nil || result.Surface != nil {
		t.Fatalf("callback result = %#v", result)
	}
	if result.Card.Header != "typed header\n\n" || result.Card.Projection.Effect != telegramui.EffectEditSameCarrier {
		t.Fatalf("projected card = %#v", result.Card)
	}
	if got := result.Card.Projection.Card.Pages[0]; got.Content != "page" || !reflect.DeepEqual(got.Anchors, []string{"history:1"}) {
		t.Fatalf("projected content = %#v", got)
	}
}

func TestTelegramControllerFlowAdapterMapsSurfaceTargetsAndMessageCards(t *testing.T) {
	const sessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	stub := &semanticControllerStub{actionResult: telegramcontroller.SemanticActionResult{Surface: &telegramcontroller.SemanticSurface{
		Text: "Архив",
		Rows: [][]telegramcontroller.SemanticButton{
			{{Label: "resume", Action: telegramcontroller.SemanticResume, SessionID: sessionID}},
			{{Label: "menu", Action: telegramcontroller.SemanticMenuBack}},
		},
	}}, messageResult: telegramcontroller.SemanticActionResult{Card: &telegramcontroller.SemanticCard{
		SessionID: sessionID, Effect: telegramcontroller.SemanticEditSameCarrier,
		Header: "new card\n", Pages: []telegramcontroller.SemanticContentPage{{Content: "body", Anchors: []string{"a"}}},
		View: telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "a", FollowLatest: true},
	}}}
	adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: stub}

	callback, err := adapter.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
		OperationID: "status:42", UpdateID: 42,
		SessionID: domain.SessionID(telegramui.GlobalSurfaceID), Action: telegramui.ActionMenuArchive,
		Effect: telegrampipeline.EffectOpenArchive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.action.SessionID != "" || stub.action.Kind != telegramcontroller.SemanticMenuArchive || stub.action.UpdateID != 42 {
		t.Fatalf("global semantic action = %#v", stub.action)
	}
	if callback.Surface == nil || !reflect.DeepEqual(callback.Surface.SelectableSessionIDs, []domain.SessionID{sessionID}) {
		t.Fatalf("surface targets = %#v", callback.Surface)
	}
	wantResume := telegramui.Button{Action: telegramui.ActionResume, Target: telegramui.ButtonTarget{SessionSlot: 1}}
	if got := callback.Surface.Keyboard.Rows[0][0]; !reflect.DeepEqual(got, wantResume) {
		t.Fatalf("resume button = %#v, want %#v", got, wantResume)
	}

	message, err := adapter.HandleMessage(context.Background(), coordinator.Update{ID: 43, Kind: coordinator.UpdateMessage})
	if err != nil {
		t.Fatal(err)
	}
	if message.Card == nil || message.Card.Projection.Effect != telegramui.EffectSendOneNewCard {
		t.Fatalf("message card = %#v", message.Card)
	}
}

func TestTelegramControllerFlowAdapterLeavesTextOnlyMessageWithoutKeyboard(t *testing.T) {
	want := coordinator.Decision{Kind: coordinator.DecisionStatus, Status: coordinator.Status{ConversationID: 42, Text: "Неизвестная команда"}}
	stub := &semanticControllerStub{messageResult: telegramcontroller.SemanticActionResult{
		Decision: want,
		Surface:  &telegramcontroller.SemanticSurface{Text: want.Status.Text},
	}}
	result, err := (telegramruntimecomposition.ControllerFlowAdapter{Controller: stub}).HandleMessage(
		context.Background(), coordinator.Update{ID: 44, Kind: coordinator.UpdateMessage},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Decision, want) || result.Card != nil || result.Surface != nil {
		t.Fatalf("message result = %#v, want text-only decision %#v", result, want)
	}
}

func TestTelegramStatusDeliveryRunnerSweepsImmediatelyAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stub := &statusDeliveryStub{swept: make(chan struct{}, 1)}
	delivered := true
	runner := telegramruntimecomposition.StatusDeliveryRunner{
		Delivery: stub, Checkpoints: checkpointLoadStub{},
		Confirmer: &orderingConfirmerStub{delivered: &delivered, called: make(chan struct{}, 1)},
		Interval:  time.Hour, Limit: 100,
		Report: func(error) { t.Fatal("unexpected delivery error") },
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-stub.swept:
	case <-time.After(time.Second):
		t.Fatal("status delivery did not sweep immediately")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestTelegramStatusDeliveryRunnerConfirmsOuterCheckpointInSameSweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := false
	delivery := &statusDeliveryStub{
		swept: make(chan struct{}, 1),
		afterSweep: func() {
			delivered = true
		},
	}
	confirmer := &orderingConfirmerStub{delivered: &delivered, called: make(chan struct{}, 1)}
	runner := telegramruntimecomposition.StatusDeliveryRunner{
		Delivery: delivery,
		Checkpoints: checkpointLoadStub{found: true, stored: coordinator.StoredCheckpoint{Checkpoint: coordinator.Checkpoint{
			Outbound: &coordinator.Outbound{OperationID: "status:100", UpdateID: 100, Phase: coordinator.OutboundEnqueued},
		}}},
		Confirmer: confirmer,
		Interval:  time.Hour,
		Limit:     100,
		Report:    func(err error) { t.Errorf("unexpected runner error: %v", err) },
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-confirmer.called:
	case <-time.After(time.Second):
		t.Fatal("outer checkpoint was not confirmed in the delivery sweep")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}
