package authflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/authflow"
)

func TestFileStoreReopensReplayFenceWithoutSecretContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-operations.json")
	store, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	provider := &fakeAuthenticator{beginResult: validBeginResult()}
	service := mustService(t, store, provider, &fakeDeleter{})
	start, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := service.Submit(context.Background(), validSubmitRequest(start.ChallengeReference)); err != nil {
		t.Fatalf("submit: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read physical store: %v", err)
	}
	if strings.Contains(string(raw), secretSentinel) {
		t.Fatalf("physical store contains secret: %s", raw)
	}

	reopened, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	replayedService := mustService(t, reopened, provider, &fakeDeleter{})
	replayed, err := replayedService.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatalf("replay start: %v", err)
	}
	if !replayed.Replayed || replayed.Status != authflow.StatusAuthenticated {
		t.Fatalf("reopened replay = %#v", replayed)
	}
	if provider.beginCalls != 1 || provider.completeCalls != 1 {
		t.Fatalf("reopen repeated provider effects: begin=%d complete=%d", provider.beginCalls, provider.completeCalls)
	}
}

func TestFileStoreRejectsCorruptOrUnknownState(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"records":`,
		`{"version":1,"records":{},"secret":"must-not-be-accepted"}`,
		`{"version":2,"records":{}}`,
	} {
		path := filepath.Join(t.TempDir(), "auth-operations.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := authflow.OpenFileStore(path); err == nil {
			t.Fatalf("OpenFileStore accepted invalid state %q", body)
		}
	}
}

func TestFileStoreRejectsSymlinkStatePath(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"records":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "auth-operations.json")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := authflow.OpenFileStore(path); err == nil {
		t.Fatal("OpenFileStore accepted a symlink state path")
	}
}

func TestFileStoreRejectsImpossibleAuthorizationState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-operations.json")
	body := `{"version":1,"records":{"op":{"operation_id":"op","revision":1,"owner_id":42,"private_chat_id":42,"computer_id":"computer-a","provider":"codex","status":"authenticated","deletion":"not_required","created_at":"2026-09-03T12:00:00Z","updated_at":"2026-09-03T12:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := authflow.OpenFileStore(path); err == nil {
		t.Fatal("OpenFileStore accepted authenticated state without a completed challenge")
	}
}

func TestFileStorePrunesOnlyOldTerminalReplayFences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-operations.json")
	store, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	for _, record := range []authflow.Record{
		terminalRecord("old-terminal", old),
		terminalRecord("recent-terminal", recent),
		unconfirmedTerminalRecord("old-unconfirmed", old),
		startingRecord("old-active", old),
	} {
		if _, created, err := store.Create(context.Background(), record); err != nil || !created {
			t.Fatalf("create %s: created=%v err=%v", record.OperationID, created, err)
		}
	}
	removed, err := store.PruneTerminalBefore(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || removed != 1 {
		t.Fatalf("prune: removed=%d err=%v", removed, err)
	}
	reopened, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := reopened.Load(context.Background(), "old-terminal"); found {
		t.Fatal("old terminal replay fence was not pruned")
	}
	for _, operationID := range []string{"recent-terminal", "old-unconfirmed", "old-active"} {
		if _, found, err := reopened.Load(context.Background(), operationID); err != nil || !found {
			t.Fatalf("preserved operation %s: found=%v err=%v", operationID, found, err)
		}
	}
}

func TestFileStoreListsPendingAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-operations.json")
	store, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	service := mustService(t, store, &fakeAuthenticator{beginResult: validBeginResult()}, &fakeDeleter{})
	if _, err := service.Start(context.Background(), validStartRequest()); err != nil {
		t.Fatal(err)
	}
	reopened, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restored := mustService(t, reopened, &fakeAuthenticator{beginResult: validBeginResult()}, &fakeDeleter{})
	pending, err := restored.Pending(context.Background(), authflow.PendingRequest{ActorID: 42, ChatID: 42, ConversationKind: "private"})
	if err != nil || len(pending) != 1 || pending[0].OperationID != "auth-op-1" {
		t.Fatalf("reopened Pending() = (%#v, %v)", pending, err)
	}
}

func TestFileStoreRestoresUnconfirmedDeletionIntentForExplicitRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-operations.json")
	store, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	failing := &fakeDeleter{err: errors.New("ambiguous Telegram delete")}
	service := mustService(t, store, &fakeAuthenticator{beginResult: validBeginResult()}, failing)
	request := authflow.DiscardRequest{OperationID: "telegram-message:913:delete", ActorID: 42, ChatID: 42, ConversationKind: "private", MessageID: 913}
	if result, err := service.Discard(context.Background(), request); !errors.Is(err, authflow.ErrDeletionUnconfirmed) || result.Deletion != authflow.DeletionUnconfirmed {
		t.Fatalf("first Discard() = (%#v, %v)", result, err)
	}
	reopened, err := authflow.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	succeeding := &fakeDeleter{}
	restored := mustService(t, reopened, &fakeAuthenticator{beginResult: validBeginResult()}, succeeding)
	if result, err := restored.Discard(context.Background(), request); err != nil || result.Deletion != authflow.DeletionConfirmed {
		t.Fatalf("retried Discard() = (%#v, %v)", result, err)
	}
	if succeeding.calls != 1 || succeeding.lastMessageID != 913 {
		t.Fatalf("retry delete calls/target = %d/%d", succeeding.calls, succeeding.lastMessageID)
	}
}

func TestFileStoreRestoresTerminalMessageTombstonesForDeletionOnly(t *testing.T) {
	for _, status := range []authflow.Status{authflow.StatusAuthenticated, authflow.StatusRejected, authflow.StatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth-operations.json")
			store, err := authflow.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			// mustService uses 2026-09-03T12:00:00Z. The persisted terminal
			// record precedes that controlled clock so the deletion-only retry is
			// a forward state transition, as it is in production.
			record := unconfirmedTerminalRecord("auth-op-terminal", time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC))
			record.Status = status
			if _, created, err := store.Create(context.Background(), record); err != nil || !created {
				t.Fatalf("Create() = created %t, err %v", created, err)
			}
			reopened, err := authflow.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			provider := &fakeAuthenticator{beginResult: validBeginResult()}
			deleter := &fakeDeleter{}
			service := mustService(t, reopened, provider, deleter)
			result, err := service.ConsumeMessage(context.Background(), authflow.MessageRequest{
				ActorID: 42, ChatID: 42, ConversationKind: "private", MessageID: 99,
			})
			if err != nil || !result.Bound || result.Status != status || result.Deletion != authflow.DeletionConfirmed {
				t.Fatalf("ConsumeMessage() = (%#v, %v)", result, err)
			}
			if provider.completeCalls != 0 || deleter.calls != 1 {
				t.Fatalf("terminal tombstone effects: provider=%d delete=%d", provider.completeCalls, deleter.calls)
			}
		})
	}
}

// A terminal authorization is a replay fence, but an unconfirmed Telegram
// deletion remains safely retryable after restart. The store must therefore
// durably accept the deletion-only transition for every terminal outcome.
func TestFileStoreConfirmsTerminalTombstoneDeletion(t *testing.T) {
	for _, status := range []authflow.Status{authflow.StatusAuthenticated, authflow.StatusRejected, authflow.StatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth-operations.json")
			store, err := authflow.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			record := unconfirmedTerminalRecord("auth-op-terminal", time.Now().UTC())
			record.Status = status
			stored, created, err := store.Create(context.Background(), record)
			if err != nil || !created {
				t.Fatalf("Create() = (%#v, %t, %v)", stored, created, err)
			}

			stored.Deletion = authflow.DeletionConfirmed
			stored.UpdatedAt = time.Now().UTC()
			confirmed, swapped, err := store.CompareAndSwap(context.Background(), stored.OperationID, stored.Revision, stored)
			if err != nil || !swapped || confirmed.Deletion != authflow.DeletionConfirmed {
				t.Fatalf("CompareAndSwap() = (%#v, %t, %v)", confirmed, swapped, err)
			}

			reopened, err := authflow.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			persisted, found, err := reopened.Load(context.Background(), record.OperationID)
			if err != nil || !found || persisted.Deletion != authflow.DeletionConfirmed || persisted.Status != status {
				t.Fatalf("reopened Load() = (%#v, %t, %v)", persisted, found, err)
			}
		})
	}
}

func terminalRecord(operationID string, at time.Time) authflow.Record {
	return authflow.Record{
		OperationID: operationID, OwnerID: 42, PrivateChatID: 42, ComputerID: "computer-a",
		Provider: authflow.ProviderCodex, Status: authflow.StatusRejected,
		Deletion: authflow.DeletionNotRequired, CreatedAt: at, UpdatedAt: at,
	}
}

func startingRecord(operationID string, at time.Time) authflow.Record {
	record := terminalRecord(operationID, at)
	record.Status = authflow.StatusStarting
	return record
}

func unconfirmedTerminalRecord(operationID string, at time.Time) authflow.Record {
	record := terminalRecord(operationID, at)
	record.ChallengeReference = "challenge-1"
	record.ExpiresAt = at.Add(time.Hour)
	record.SubmissionOperationID = "submit-1"
	record.SecretMessageReference = authflow.SecretMessageReference{ChatID: 42, MessageID: 99}
	record.Deletion = authflow.DeletionUnconfirmed
	return record
}
