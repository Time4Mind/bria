package telegramflow_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestCallbackStateSnapshotCarriesUnknownOperationWithoutSigningKeySurface(t *testing.T) {
	store, err := telegramflow.OpenFileCallbackOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	operation := telegramflow.CallbackOperation{
		ID: "callback-operation-1", UpdateID: 10,
		CallbackQueryID: "callback-query-1",
		CallbackDigest:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Plan: telegrampipeline.CallbackPlan{
			OperationID: "callback-operation-1", UpdateID: 10,
			SessionID: "session-1", Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 10},
			Action: telegramui.ActionOptions, Effect: telegrampipeline.EffectToggleOptions,
		},
		Phase: telegramflow.CallbackClaimed,
	}
	if err := store.Create(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	unknown := operation
	unknown.Phase = telegramflow.CallbackEffectUnknown
	changed, err := store.CompareAndSwap(context.Background(), operation.ID, telegramflow.CallbackClaimed, unknown)
	if err != nil || !changed {
		t.Fatalf("mark unknown changed=%v err=%v", changed, err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Operations[operation.ID].Phase != telegramflow.CallbackEffectUnknown || snapshot.Statuses == nil {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}
