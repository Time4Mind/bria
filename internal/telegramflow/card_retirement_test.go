package telegramflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type newCardMessageExecutor struct{ card telegramflow.CardOutput }

func (executor newCardMessageExecutor) HandleMessage(context.Context, coordinator.Update) (telegramflow.MessageResult, error) {
	card := executor.card
	return telegramflow.MessageResult{Card: &card}, nil
}

type cardRetirementWire struct {
	*sender
	events    []string
	retireErr error
}

func (wire *cardRetirementWire) DeactivateInlineKeyboard(context.Context, string, int64, int64) error {
	wire.events = append(wire.events, "retire")
	return wire.retireErr
}

func (wire *cardRetirementWire) SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	wire.events = append(wire.events, "send")
	return coordinator.Receipt{MessageID: 900}, nil
}

func newCardOutput(t *testing.T) telegramflow.CardOutput {
	t.Helper()
	projection, err := telegramui.ProjectActiveFinal(telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "latest", Anchors: []string{"latest"}}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "latest", FollowLatest: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return telegramflow.CardOutput{SessionID: flowSessionID, Projection: projection, MakeActive: true}
}

func newCardRetirementFlow(t *testing.T, wire *cardRetirementWire, operations telegramflow.CallbackOperationStore) (*telegramflow.Handler, *telegramflow.Sender, telegramstate.Store) {
	t.Helper()
	ctx := context.Background()
	state := telegramstate.NewMemoryStore()
	if err := state.Update(ctx, func(snapshot *telegramstate.State) error {
		snapshot.ActiveSession = flowSessionID
		return snapshot.SetCard(telegramstate.Card{
			SessionID: flowSessionID,
			Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 100},
			Page:      telegramstate.Page{Current: 1, Total: 1, FollowLatest: true},
		})
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, now),
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          state, MessageUI: newCardMessageExecutor{card: newCardOutput(t)}, Callbacks: &callbackExecutor{},
		Operations: operations, Sender: wire,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, outbound, state
}

func TestNewInputRetiresPreviousCarrierBeforeDurableSendFence(t *testing.T) {
	ctx := context.Background()
	operations := telegramflow.NewMemoryCallbackOperationStore()
	wire := &cardRetirementWire{sender: &sender{}}
	handler, outbound, _ := newCardRetirementFlow(t, wire, operations)
	decision, err := handler.Handle(ctx, coordinator.Update{ID: 41, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EnqueueStatus(ctx, "status:41", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	if got, want := wire.events, []string{"retire", "send"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestActiveFinalRetiresPreviousCarrierBeforeNewCardSend(t *testing.T) {
	ctx := context.Background()
	operations := telegramflow.NewMemoryCallbackOperationStore()
	wire := &cardRetirementWire{sender: &sender{}}
	_, outbound, state := newCardRetirementFlow(t, wire, operations)
	prepared, err := telegramflow.PrepareCompletion("completion:active", flowSessionID, 42, true, "", telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "final", Anchors: []string{"final"}}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true},
	}, false, nil, newPresenter(t, time.Unix(1_800_000_000, 0).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = telegramflow.CapturePreviousCarrier(ctx, state, prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.SendStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatal(err)
	}
	if got, want := wire.events, []string{"retire", "send"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestRetirementFailureLeavesStatusQueuedAndDoesNotSend(t *testing.T) {
	ctx := context.Background()
	operations := telegramflow.NewMemoryCallbackOperationStore()
	wire := &cardRetirementWire{sender: &sender{}, retireErr: errors.New("unavailable")}
	handler, outbound, _ := newCardRetirementFlow(t, wire, operations)
	decision, err := handler.Handle(ctx, coordinator.Update{ID: 42, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EnqueueStatus(ctx, "status:42", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	operation, found, err := operations.LoadStatus(ctx, "status:42")
	if err != nil || !found || operation.Phase != telegramflow.StatusQueued {
		t.Fatalf("operation = %#v, found=%t, err=%v", operation, found, err)
	}
	if got, want := wire.events, []string{"retire"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if err := outbound.DeliverPendingStatuses(ctx, 10); err == nil {
		t.Fatal("retry unexpectedly ignored retirement failure")
	}
}

func TestDelayedReplayDoesNotRetireReplacementCarrier(t *testing.T) {
	ctx := context.Background()
	operations := telegramflow.NewMemoryCallbackOperationStore()
	wire := &cardRetirementWire{sender: &sender{}}
	_, outbound, state := newCardRetirementFlow(t, wire, operations)
	prepared, err := telegramflow.PrepareCompletion("status:43", flowSessionID, 42, true, "", telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "final", Anchors: []string{"final"}}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "final", FollowLatest: true},
	}, false, nil, newPresenter(t, time.Unix(1_800_000_000, 0).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = telegramflow.CapturePreviousCarrier(ctx, state, prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := operations.EnqueueStatus(ctx, telegramflow.StatusOperation{ID: prepared.OperationID, Sequence: 43, Status: prepared.Status, Keyboard: prepared.Keyboard, Prepared: &prepared, Phase: telegramflow.StatusQueued}); err != nil {
		t.Fatal(err)
	}
	if err := state.Update(ctx, func(snapshot *telegramstate.State) error {
		current, _ := snapshot.Card(flowSessionID)
		current.Carrier = telegramstate.Carrier{ChatID: 42, MessageID: 901}
		return snapshot.SetCard(current)
	}); err != nil {
		t.Fatal(err)
	}
	if err := outbound.DeliverPendingStatuses(ctx, 10); err != nil {
		t.Fatalf("delivery error = %v", err)
	}
	if len(wire.events) != 0 {
		t.Fatalf("replacement carrier was touched: %v", wire.events)
	}
	operation, found, err := operations.LoadStatus(ctx, prepared.OperationID)
	if err != nil || !found || operation.Phase != telegramflow.StatusSuperseded {
		t.Fatalf("stale operation = %#v, found=%t, err=%v", operation, found, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
