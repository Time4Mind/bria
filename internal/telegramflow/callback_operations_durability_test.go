package telegramflow

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestCallbackOperationStoreAcknowledgesTransitionOnlyAfterDirectorySync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "callback-operations.json")
	store, err := OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	directorySyncFailure := errors.New("directory sync unavailable")
	syncCalls := 0
	store.syncDirectory = func(directory string) error {
		syncCalls++
		if directory != filepath.Dir(path) {
			t.Fatalf("synced directory=%q want %q", directory, filepath.Dir(path))
		}
		return directorySyncFailure
	}
	op := CallbackOperation{
		ID: "status:1", UpdateID: 1, CallbackQueryID: "query-1",
		CallbackDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Plan:           callbackPlanForDurabilityTest(), Phase: CallbackClaimed,
	}
	if err := store.Create(context.Background(), op); !errors.Is(err, directorySyncFailure) {
		t.Fatalf("Create() error=%v want directory sync failure", err)
	}
	if syncCalls != 1 {
		t.Fatalf("directory sync calls=%d want 1", syncCalls)
	}

	// Rename precedes directory fsync. If the new name is observable, restart
	// must read one complete validated snapshot, never a partial JSON file.
	reopened, err := OpenFileCallbackOperationStore(path)
	if err != nil {
		t.Fatalf("reopen post-rename snapshot: %v", err)
	}
	got, ok, err := reopened.Load(context.Background(), op.ID)
	if err != nil || !ok || got.Phase != CallbackClaimed {
		t.Fatalf("reopened operation=%#v ok=%t err=%v", got, ok, err)
	}
}

func callbackPlanForDurabilityTest() telegrampipeline.CallbackPlan {
	return telegrampipeline.CallbackPlan{
		OperationID: "status:1",
		UpdateID:    1,
		SessionID:   "123e4567-e89b-12d3-a456-426614174000",
		Carrier:     telegramstate.Carrier{ChatID: 42, MessageID: 99},
		Action:      telegramui.ActionOptions,
		Effect:      telegrampipeline.EffectToggleOptions,
	}
}
