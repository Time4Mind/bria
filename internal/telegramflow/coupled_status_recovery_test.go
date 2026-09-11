package telegramflow_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestReceiptConfirmedStatusAutomaticallySettlesCoupledCallbackAfterRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	state := telegramstate.NewMemoryStore()
	const id domain.SessionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if err := state.Update(ctx, func(snapshot *telegramstate.State) error {
		snapshot.ActiveSession = id
		return snapshot.SetCard(telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 202}, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCardRefresh("status:901", id, 42, 202,
		telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "recovered"}}, View: telegramui.PageView{Page: 1, Pages: 1}},
		"", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	plan := telegrampipeline.CallbackPlan{OperationID: prepared.OperationID, UpdateID: 901, SessionID: id,
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 202}, Action: telegramui.ActionPagePrevious,
		Target: telegramui.ButtonTarget{Page: 1}, Effect: telegrampipeline.EffectProjectPage}
	operations := telegramflow.NewMemoryCallbackOperationStore()
	for sequence := 1; sequence <= 100; sequence++ {
		unknownStatus := telegramflow.StatusOperation{
			ID: fmt.Sprintf("status:%d", sequence), Sequence: uint64(sequence),
			Status: coordinator.Status{ConversationID: 42, Text: "genuinely unknown"}, Phase: telegramflow.StatusQueued,
		}
		if _, _, err := operations.EnqueueStatus(ctx, unknownStatus); err != nil {
			t.Fatal(err)
		}
		fenced := unknownStatus
		fenced.Phase = telegramflow.StatusSendUnknown
		if changed, err := operations.CompareAndSwapStatus(ctx, unknownStatus.ID, unknownStatus.Phase, fenced); err != nil || !changed {
			t.Fatalf("unknown status %d = %t, %v", sequence, changed, err)
		}
	}
	callback := telegramflow.CallbackOperation{ID: prepared.OperationID, UpdateID: 901, CallbackQueryID: "query-901",
		CallbackDigest: strings.Repeat("a", 64), Plan: plan, Phase: telegramflow.CallbackClaimed}
	if err := operations.Create(ctx, callback); err != nil {
		t.Fatal(err)
	}
	effectUnknown := callback
	effectUnknown.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := operations.CompareAndSwap(ctx, callback.ID, callback.Phase, effectUnknown); err != nil || !changed {
		t.Fatalf("callback effect fence = %t, %v", changed, err)
	}
	callbackPrepared := effectUnknown
	callbackPrepared.Phase, callbackPrepared.Prepared = telegramflow.CallbackPrepared, &prepared
	if changed, err := operations.CompareAndSwap(ctx, callback.ID, effectUnknown.Phase, callbackPrepared); err != nil || !changed {
		t.Fatalf("callback prepared = %t, %v", changed, err)
	}
	callbackUnknown := callbackPrepared
	callbackUnknown.Phase = telegramflow.CallbackSendUnknown
	if changed, err := operations.CompareAndSwap(ctx, callback.ID, callbackPrepared.Phase, callbackUnknown); err != nil || !changed {
		t.Fatalf("callback send fence = %t, %v", changed, err)
	}
	status := telegramflow.StatusOperation{ID: prepared.OperationID, Sequence: 901, Status: prepared.Status, Keyboard: prepared.Keyboard,
		Prepared: &prepared, Edit: true, Phase: telegramflow.StatusQueued}
	if _, _, err := operations.EnqueueStatus(ctx, status); err != nil {
		t.Fatal(err)
	}
	statusUnknown := status
	statusUnknown.Phase = telegramflow.StatusSendUnknown
	if changed, err := operations.CompareAndSwapStatus(ctx, status.ID, status.Phase, statusUnknown); err != nil || !changed {
		t.Fatalf("status send fence = %t, %v", changed, err)
	}
	statusConfirmed := statusUnknown
	statusConfirmed.Phase, statusConfirmed.Receipt = telegramflow.StatusReceiptConfirmed, 202
	if changed, err := operations.CompareAndSwapStatus(ctx, status.ID, statusUnknown.Phase, statusConfirmed); err != nil || !changed {
		t.Fatalf("status receipt = %t, %v", changed, err)
	}
	transport := &sender{receipt: coordinator.Receipt{MessageID: 202}}
	_, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: state, MessageUI: semanticMessageHandler{}, Callbacks: backgroundFinalNavigationExecutor{target: id},
		Operations: operations, Sender: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.DeliverPendingStatuses(ctx, 10); err != nil {
		t.Fatal(err)
	}
	settledCallback, found, err := operations.Load(ctx, prepared.OperationID)
	if err != nil || !found || settledCallback.Phase != telegramflow.CallbackCommitted || settledCallback.Receipt != 202 {
		t.Fatalf("callback did not converge = %+v found=%t err=%v", settledCallback, found, err)
	}
	settledStatus, found, err := operations.LoadStatus(ctx, prepared.OperationID)
	if err != nil || !found || settledStatus.Phase != telegramflow.StatusCommitted || settledStatus.Receipt != 202 {
		t.Fatalf("status did not converge = %+v found=%t err=%v", settledStatus, found, err)
	}
	if transport.edits != 0 || transport.sends != 0 {
		t.Fatalf("known receipt repeated Telegram mutation: edits=%d sends=%d", transport.edits, transport.sends)
	}
}

func TestLateBackgroundFinalRetryCannotReplaceNewerActiveSession(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	state := telegramstate.NewMemoryStore()
	const (
		previousID domain.SessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		targetID   domain.SessionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		newerID    domain.SessionID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	)
	previous := telegramstate.Carrier{ChatID: 42, MessageID: 101}
	if err := state.Update(ctx, func(snapshot *telegramstate.State) error {
		snapshot.ActiveSession = previousID
		for _, card := range []telegramstate.Card{
			{SessionID: previousID, Carrier: previous, Page: telegramstate.Page{Current: 1, Total: 1}},
			{SessionID: targetID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 202}, Page: telegramstate.Page{Current: 1, Total: 1}},
			{SessionID: newerID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 303}, Page: telegramstate.Page{Current: 1, Total: 1}},
		} {
			if err := snapshot.SetCard(card); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCardRefresh("status:902", targetID, 42, 202,
		telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "background final"}}, View: telegramui.PageView{Page: 1, Pages: 1}},
		"", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Card.MakeActive = true
	prepared.PreviousActiveSessionID, prepared.PreviousActiveCarrier = previousID, &previous
	operations := telegramflow.NewMemoryCallbackOperationStore()
	callback := telegramflow.CallbackOperation{ID: prepared.OperationID, UpdateID: 902, CallbackQueryID: "query-902",
		CallbackDigest: strings.Repeat("b", 64), Phase: telegramflow.CallbackClaimed,
		Plan: telegrampipeline.CallbackPlan{OperationID: prepared.OperationID, UpdateID: 902, SessionID: targetID,
			Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 202}, Action: telegramui.ActionSelectSession,
			Target: telegramui.ButtonTarget{}, Effect: telegrampipeline.EffectSelectSession}}
	if err := operations.Create(ctx, callback); err != nil {
		t.Fatal(err)
	}
	unknown := callback
	unknown.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := operations.CompareAndSwap(ctx, callback.ID, callback.Phase, unknown); err != nil || !changed {
		t.Fatalf("effect fence = %t, %v", changed, err)
	}
	preparedOperation := unknown
	preparedOperation.Phase, preparedOperation.Prepared = telegramflow.CallbackPrepared, &prepared
	if changed, err := operations.CompareAndSwap(ctx, callback.ID, unknown.Phase, preparedOperation); err != nil || !changed {
		t.Fatalf("prepared callback = %t, %v", changed, err)
	}
	wire := &backgroundFinalNavigationWire{sender: &sender{receipt: coordinator.Receipt{MessageID: 202}, err: errors.New("timeout after write")}, keyboards: map[telegramstate.Carrier]int{previous: 1}}
	_, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: registry, UIState: state, MessageUI: semanticMessageHandler{}, Callbacks: backgroundFinalNavigationExecutor{target: targetID},
		Operations: operations, Sender: wire})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err == nil {
		t.Fatal("initial unknown delivery returned no error")
	}
	if err := state.Update(ctx, func(snapshot *telegramstate.State) error { snapshot.ActiveSession = newerID; return nil }); err != nil {
		t.Fatal(err)
	}
	wire.sender.err = nil
	if err := outbound.RetryUnknownSend(ctx, prepared.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err == nil {
		t.Fatal("late retry replaced the newer active session")
	}
	got, err := state.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveSession != newerID || wire.sender.edits != 1 {
		t.Fatalf("late retry changed newer view: active=%s edits=%d", got.ActiveSession, wire.sender.edits)
	}
}
