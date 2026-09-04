package telegramflow_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
)

func TestCallbackAcknowledgementIsIndependentAndNeverReplayedAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCallbackAcknowledgement(context.Background(), "status:1", "query-1"); err != nil {
		t.Fatal(err)
	}
	allowed, err := store.BeginCallbackAcknowledgement(context.Background(), "status:1", "query-1")
	if err != nil || !allowed {
		t.Fatalf("fresh Begin = %t, %v", allowed, err)
	}
	if err := store.CompleteCallbackAcknowledgement(context.Background(), "status:1", "query-1", telegrambridge.CallbackAcknowledgementConfirmed); err != nil {
		t.Fatal(err)
	}
	confirmed, found, err := store.LoadCallbackAcknowledgement(context.Background(), "status:1")
	if err != nil || !found || confirmed.Phase != telegramflow.CallbackAcknowledgementConfirmed {
		t.Fatalf("confirmed acknowledgement = (%#v, %t, %v)", confirmed, found, err)
	}

	if err := store.CreateCallbackAcknowledgement(context.Background(), "status:2", "query-2"); err != nil {
		t.Fatal(err)
	}
	reopened, err := telegramflow.OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.CreateCallbackAcknowledgement(context.Background(), "status:2", "query-2"); err != nil {
		t.Fatalf("idempotent recovered Create: %v", err)
	}
	allowed, err = reopened.BeginCallbackAcknowledgement(context.Background(), "status:2", "query-2")
	if err != nil || allowed {
		t.Fatalf("reopened Begin = %t, %v; want false", allowed, err)
	}
	abandoned, found, err := reopened.LoadCallbackAcknowledgement(context.Background(), "status:2")
	if err != nil || !found || abandoned.Phase != telegramflow.CallbackAcknowledgementAbandoned {
		t.Fatalf("abandoned acknowledgement = (%#v, %t, %v)", abandoned, found, err)
	}
}

func TestCallbackAcknowledgementFailureDoesNotChangeCallbackOperationState(t *testing.T) {
	store := telegramflow.NewMemoryCallbackOperationStore()
	if err := store.CreateCallbackAcknowledgement(context.Background(), "status:3", "query-3"); err != nil {
		t.Fatal(err)
	}
	allowed, err := store.BeginCallbackAcknowledgement(context.Background(), "status:3", "query-3")
	if err != nil || !allowed {
		t.Fatalf("Begin = %t, %v", allowed, err)
	}
	if err := store.CompleteCallbackAcknowledgement(context.Background(), "status:3", "query-3", telegrambridge.CallbackAcknowledgementFailed); err != nil {
		t.Fatal(err)
	}
	acknowledgement, found, err := store.LoadCallbackAcknowledgement(context.Background(), "status:3")
	if err != nil || !found || acknowledgement.Phase != telegramflow.CallbackAcknowledgementFailed {
		t.Fatalf("failed acknowledgement = (%#v, %t, %v)", acknowledgement, found, err)
	}
}
