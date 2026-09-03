package interactionstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
)

func TestFileStoreDurablyReopensExactInteractionAndFencedPhase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "provider-interactions.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	operation := testOperation()
	created, err := store.Create(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Phase != PhasePrepared {
		t.Fatalf("created operation = %#v", created)
	}
	fenced := created
	fenced.Phase = PhaseSendUnknown
	fenced, changed, err := store.CompareAndSwap(context.Background(), created.ID, created.Revision, fenced)
	if err != nil || !changed {
		t.Fatalf("fence send = (%#v, %t, %v)", fenced, changed, err)
	}
	providerUnknown := testOperation()
	providerUnknown.ID = "interaction:opaque-2"
	providerUnknown.MessageID = "telegram-update:8"
	providerUnknown.QuestionIndex = 1
	providerUnknown.Answers = map[string][]string{"choice": {"A"}}
	providerUnknown.CarrierMessageID = 92
	providerUnknown.Phase = PhaseProviderResponseUnknown
	providerUnknown.Response = &runtimeprotocol.InteractionResponse{
		ID: providerUnknown.ProviderRequestID, Outcome: runtimeprotocol.OutcomeAnswered,
		Answers: map[string][]string{"choice": {"A"}},
	}
	if _, err := store.Create(context.Background(), providerUnknown); err != nil {
		t.Fatalf("create provider-unknown operation: %v", err)
	}

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.Load(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("reopen load = (%#v, %t, %v)", got, found, err)
	}
	if !reflect.DeepEqual(got, fenced) || got.Revision != 2 {
		t.Fatalf("reopened = %#v, want %#v", got, fenced)
	}
	gotProviderUnknown, found, err := reopened.Load(context.Background(), providerUnknown.ID)
	if err != nil || !found || gotProviderUnknown.Phase != PhaseProviderResponseUnknown ||
		!reflect.DeepEqual(gotProviderUnknown.Response, providerUnknown.Response) {
		t.Fatalf("reopened provider response fence = (%#v, %t, %v)", gotProviderUnknown, found, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v", info.Mode().Perm())
	}
}

func TestFileStoreRejectsCorruptionAndImmutableIdentityChange(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "provider-interactions.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"operations":{"bad":{"id":"wrong"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(path); err == nil {
		t.Fatal("OpenFileStore accepted corrupt operation")
	}

	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testOperation())
	if err != nil {
		t.Fatal(err)
	}
	changedIdentity := created
	changedIdentity.MessageID = "different-message"
	changedIdentity.Phase = PhaseSendUnknown
	if _, _, err := store.CompareAndSwap(context.Background(), created.ID, created.Revision, changedIdentity); !errors.Is(err, ErrImmutableIdentity) {
		t.Fatalf("CompareAndSwap identity error = %v, want ErrImmutableIdentity", err)
	}
}

func TestFileStoreReopenPrunesDurablyConfirmedResponseAfterCrashGap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "provider-interactions.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := testOperation()
	confirmed.ID = "interaction:confirmed"
	confirmed.Phase = PhaseProviderResponseConfirmed
	confirmed.CarrierMessageID = 91
	confirmed.QuestionIndex = 1
	confirmed.Answers = map[string][]string{"choice": {"A"}}
	confirmed.Response = &runtimeprotocol.InteractionResponse{
		ID: confirmed.ProviderRequestID, Outcome: runtimeprotocol.OutcomeAnswered,
		Answers: map[string][]string{"choice": {"A"}},
	}
	if _, err := store.Create(context.Background(), confirmed); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reopened.Load(context.Background(), confirmed.ID); err != nil || found {
		t.Fatalf("confirmed crash-gap record survived reopen: found=%t err=%v", found, err)
	}
}

func TestFileStoreRetainsConsumedSecretSourceAcrossOperationPruneAndReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "provider-interactions.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	source, err := store.RecordConsumedSource(context.Background(), ConsumedSource{
		OperationID: "interaction:secret", ActorID: 7, ConversationID: 42, MessageID: 103,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	unknown, found, err := reopened.LoadConsumedSource(context.Background(), 7, 42, 103)
	if err != nil || !found || unknown.DeletionKnown {
		t.Fatalf("unknown deletion tombstone = (%#v,%t,%v)", unknown, found, err)
	}
	source.UpdatedAt = now.Add(time.Second)
	confirmed, changed, err := reopened.ConfirmConsumedSourceDeletion(context.Background(), source, source.Revision)
	if err != nil || !changed || !confirmed.DeletionKnown {
		t.Fatalf("confirm deletion = (%#v,%t,%v)", confirmed, changed, err)
	}
	reopened, err = OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, found, err = reopened.LoadConsumedSource(context.Background(), 7, 42, 103)
	if err != nil || !found || !confirmed.DeletionKnown {
		t.Fatalf("confirmed deletion tombstone after reopen = (%#v,%t,%v)", confirmed, found, err)
	}
}

func TestMemoryStoreFailsClosedAtOperationCapacity(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	for index := 0; index < maxOperations; index++ {
		operation := testOperation()
		operation.ID = fmt.Sprintf("interaction:%d", index)
		operation.MessageID = fmt.Sprintf("message:%d", index)
		if _, err := store.Create(context.Background(), operation); err != nil {
			t.Fatalf("Create(%d) error = %v", index, err)
		}
	}
	overflow := testOperation()
	overflow.ID = "interaction:overflow"
	overflow.MessageID = "message:overflow"
	if _, err := store.Create(context.Background(), overflow); !errors.Is(err, ErrStoreExhausted) {
		t.Fatalf("overflow error = %v, want ErrStoreExhausted", err)
	}
}

func testOperation() Operation {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	return Operation{
		ID: "interaction:opaque-1", SessionID: domain.SessionID("00000000-0000-4000-8000-000000000001"),
		MessageID: "telegram-update:7", ProviderRequestID: "provider-request-1",
		ConversationID: 42, Phase: PhasePrepared, CreatedAt: now, UpdatedAt: now,
		Request: runtimeprotocol.InteractionRequest{
			ID: "provider-request-1", Kind: runtimeprotocol.InteractionQuestion,
			ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Blocking: true,
			Questions: []runtimeprotocol.Question{{ID: "choice", Header: "Choose", Text: "Pick one", Options: []runtimeprotocol.Option{{Label: "A"}, {Label: "B"}}}},
		},
		Answers: map[string][]string{},
	}
}
