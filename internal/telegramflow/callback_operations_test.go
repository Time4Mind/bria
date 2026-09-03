package telegramflow_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestFileCallbackOperationStorePersistsAndFencesEverySideEffectPhase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "callback-operations.json")
	store, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	op := telegramflow.CallbackOperation{
		ID:              "status:101",
		UpdateID:        101,
		CallbackQueryID: "query-101",
		CallbackDigest:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Plan: telegrampipeline.CallbackPlan{
			OperationID: "status:101",
			UpdateID:    101,
			SessionID:   flowSessionID,
			Carrier:     telegramstate.Carrier{ChatID: 42, MessageID: 99},
			Action:      telegramui.ActionOptions,
			Effect:      telegrampipeline.EffectToggleOptions,
		},
		Phase: telegramflow.CallbackClaimed,
	}
	if err := store.Create(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	effectUnknown := op
	effectUnknown.Phase = telegramflow.CallbackEffectUnknown
	changed, err := store.CompareAndSwap(context.Background(), op.ID, telegramflow.CallbackClaimed, effectUnknown)
	if err != nil || !changed {
		t.Fatalf("claim -> effect unknown = %t, %v", changed, err)
	}
	prepared := effectUnknown
	prepared.Phase = telegramflow.CallbackPrepared
	prepared.Prepared = &telegramflow.Prepared{
		OperationID: op.ID,
		Status:      coordinator.Status{ConversationID: 42, Text: "card", SourceMessageID: 99},
		Keyboard:    &coordinator.KeyboardMarkup{{{Text: "Опции", CallbackData: "signed"}}},
		Card: telegramflow.CardOutput{
			SessionID: flowSessionID,
			Projection: telegramui.CarrierProjection{
				Effect: telegramui.EffectEditSameCarrier,
				Card: telegramui.ProjectedCard{
					Pages: []telegramui.ContentPage{{Content: "card", Anchors: []string{"card"}}},
					View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "card", FollowLatest: true},
				},
			},
		},
	}
	// A prepared operation needs a real presentation. The store must reject a
	// partial record instead of making an unrecoverable callback look durable.
	if changed, err := store.CompareAndSwap(context.Background(), op.ID, telegramflow.CallbackEffectUnknown, prepared); err == nil || changed {
		t.Fatalf("partial prepared operation accepted: changed=%t err=%v", changed, err)
	}

	reopened, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := reopened.Load(context.Background(), op.ID)
	if err != nil || !ok || got.Phase != telegramflow.CallbackEffectUnknown || got.Plan.SessionID != domain.SessionID(flowSessionID) {
		t.Fatalf("reopened operation = %#v, %t, %v", got, ok, err)
	}
	unknowns, err := reopened.ListUnknown(context.Background(), 1)
	if err != nil || len(unknowns) != 1 || unknowns[0].ID != op.ID || unknowns[0].Phase != telegramflow.CallbackEffectUnknown {
		t.Fatalf("durable unknown list = %#v, %v", unknowns, err)
	}
	if _, err := reopened.ListUnknown(context.Background(), 101); err == nil {
		t.Fatal("unbounded unknown operation listing was accepted")
	}
}

func TestFileCallbackOperationStoreKeepsStatusOutboxAcrossReopenAndCallbackCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "callback-and-status-operations.json")
	store, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	status := telegramflow.StatusOperation{
		ID: "status:201", Sequence: 201,
		Status: coordinator.Status{ConversationID: 42, Text: "durable"},
		Phase:  telegramflow.StatusQueued,
	}
	if got, inserted, err := store.EnqueueStatus(context.Background(), status); err != nil || !inserted || got.ID != status.ID {
		t.Fatalf("enqueue status = %#v, %t, %v", got, inserted, err)
	}
	callback := telegramflow.CallbackOperation{
		ID: "status:202", UpdateID: 202, CallbackQueryID: "query-202",
		CallbackDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Plan: telegrampipeline.CallbackPlan{OperationID: "status:202", UpdateID: 202, SessionID: flowSessionID,
			Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Action: telegramui.ActionOptions, Effect: telegrampipeline.EffectToggleOptions},
		Phase: telegramflow.CallbackClaimed,
	}
	if err := store.Create(context.Background(), callback); err != nil {
		t.Fatal(err)
	}
	unknownCallback := callback
	unknownCallback.Phase = telegramflow.CallbackEffectUnknown
	if changed, err := store.CompareAndSwap(context.Background(), callback.ID, telegramflow.CallbackClaimed, unknownCallback); err != nil || !changed {
		t.Fatalf("callback CAS = %t, %v", changed, err)
	}
	reopened, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := reopened.LoadStatus(context.Background(), status.ID)
	if err != nil || !ok || got.Phase != telegramflow.StatusQueued || got.Status.Text != "durable" {
		t.Fatalf("reopened status = %#v, %t, %v", got, ok, err)
	}
	sendUnknown := got
	sendUnknown.Phase = telegramflow.StatusSendUnknown
	if changed, err := reopened.CompareAndSwapStatus(context.Background(), got.ID, telegramflow.StatusQueued, sendUnknown); err != nil || !changed {
		t.Fatalf("status CAS = %t, %v", changed, err)
	}
	if callbackGot, ok, err := reopened.Load(context.Background(), callback.ID); err != nil || !ok || callbackGot.Phase != telegramflow.CallbackEffectUnknown {
		t.Fatalf("callback lost after status CAS = %#v, %t, %v", callbackGot, ok, err)
	}
}
