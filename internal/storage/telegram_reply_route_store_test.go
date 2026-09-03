package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/storage"
)

func TestTelegramReplyRouteStorePersistsReceiptAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "telegram-reply-routes.json")
	store, err := storage.OpenTelegramReplyRouteStore(path, 42, 42)
	if err != nil {
		t.Fatalf("OpenTelegramReplyRouteStore() error = %v", err)
	}
	if err := store.RecordOutboundReceipt(context.Background(), storage.TelegramOutboundReceipt{
		MessageID: 500,
		SessionID: domain.SessionID("session-background"),
	}); err != nil {
		t.Fatalf("RecordOutboundReceipt() error = %v", err)
	}

	reopened, err := storage.OpenTelegramReplyRouteStore(path, 42, 42)
	if err != nil {
		t.Fatalf("reopen reply route store: %v", err)
	}
	got, found, err := reopened.ResolveReply(context.Background(), 500)
	if err != nil || !found || got != "session-background" {
		t.Fatalf("ResolveReply() = (%q, %t, %v), want (session-background, true, nil)", got, found, err)
	}
	if _, found, err := reopened.ResolveReply(context.Background(), 501); err != nil || found {
		t.Fatalf("ResolveReply(missing) found=%t err=%v, want false, nil", found, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted reply routes: %v", err)
	}
	var persisted struct {
		Version       int   `json:"version"`
		OwnerUserID   int64 `json:"owner_user_id"`
		PrivateChatID int64 `json:"private_chat_id"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode persisted reply routes: %v", err)
	}
	if persisted.Version != 1 || persisted.OwnerUserID != 42 || persisted.PrivateChatID != 42 {
		t.Fatalf("persisted scope = %#v, want version 1 and owner/private chat 42", persisted)
	}
}

func TestTelegramReplyRouteStoreIsIdempotentAndRejectsConflicts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "telegram-reply-routes.json")
	first, err := storage.OpenTelegramReplyRouteStore(path, 42, 99)
	if err != nil {
		t.Fatal(err)
	}
	second, err := storage.OpenTelegramReplyRouteStore(path, 42, 99)
	if err != nil {
		t.Fatal(err)
	}
	receipt := storage.TelegramOutboundReceipt{MessageID: 700, SessionID: "session-a"}
	if err := first.RecordOutboundReceipt(context.Background(), receipt); err != nil {
		t.Fatalf("first RecordOutboundReceipt() error = %v", err)
	}
	if err := second.RecordOutboundReceipt(context.Background(), receipt); err != nil {
		t.Fatalf("idempotent RecordOutboundReceipt() error = %v", err)
	}
	if err := second.RecordOutboundReceipt(context.Background(), storage.TelegramOutboundReceipt{
		MessageID: 700,
		SessionID: "session-b",
	}); !errors.Is(err, storage.ErrReplyRouteConflict) {
		t.Fatalf("conflicting RecordOutboundReceipt() error = %v, want ErrReplyRouteConflict", err)
	}

	if _, err := storage.OpenTelegramReplyRouteStore(path, 43, 99); !errors.Is(err, storage.ErrReplyRouteScope) {
		t.Fatalf("open with another owner error = %v, want ErrReplyRouteScope", err)
	}
	if _, err := storage.OpenTelegramReplyRouteStore(path, 42, 100); !errors.Is(err, storage.ErrReplyRouteScope) {
		t.Fatalf("open with another private chat error = %v, want ErrReplyRouteScope", err)
	}
}

func TestTelegramReplyRouteStoreRejectsInvalidIdentities(t *testing.T) {
	t.Parallel()

	for _, scope := range []struct {
		owner int64
		chat  int64
	}{{0, 42}, {42, 0}, {-1, 42}, {42, -1}} {
		if _, err := storage.OpenTelegramReplyRouteStore(filepath.Join(t.TempDir(), "routes.json"), scope.owner, scope.chat); err == nil {
			t.Fatalf("OpenTelegramReplyRouteStore(owner=%d, chat=%d) error=nil", scope.owner, scope.chat)
		}
	}

	store, err := storage.OpenTelegramReplyRouteStore(filepath.Join(t.TempDir(), "routes.json"), 42, 42)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []storage.TelegramOutboundReceipt{
		{MessageID: 0, SessionID: "session"},
		{MessageID: -1, SessionID: "session"},
		{MessageID: 1, SessionID: ""},
		{MessageID: 1, SessionID: " session "},
	}
	for _, receipt := range invalid {
		if err := store.RecordOutboundReceipt(context.Background(), receipt); err == nil {
			t.Fatalf("RecordOutboundReceipt(%#v) error=nil", receipt)
		}
	}
	if _, _, err := store.ResolveReply(context.Background(), 0); err == nil {
		t.Fatal("ResolveReply(0) error=nil")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.RecordOutboundReceipt(cancelled, storage.TelegramOutboundReceipt{MessageID: 1, SessionID: "session"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled RecordOutboundReceipt() error=%v, want context.Canceled", err)
	}
	if _, _, err := store.ResolveReply(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ResolveReply() error=%v, want context.Canceled", err)
	}
}

func TestTelegramReplyRouteStoreRejectsCorruptOrAmbiguousState(t *testing.T) {
	t.Parallel()

	tests := []string{
		`{"version":1,"owner_user_id":42,"owner_user_id":43,"private_chat_id":42,"routes":[]}`,
		`{"version":1,"owner_user_id":42,"private_chat_id":42,"routes":[],"unknown":true}`,
		`{"version":2,"owner_user_id":42,"private_chat_id":42,"routes":[]}`,
		`{"version":1,"owner_user_id":42,"private_chat_id":42,"routes":[{"message_id":5,"session_id":"a"},{"message_id":5,"session_id":"a"}]}`,
	}
	for _, fixture := range tests {
		path := filepath.Join(t.TempDir(), "routes.json")
		if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.OpenTelegramReplyRouteStore(path, 42, 42); err == nil {
			t.Fatalf("OpenTelegramReplyRouteStore(%s) error=nil", fixture)
		}
	}
}
