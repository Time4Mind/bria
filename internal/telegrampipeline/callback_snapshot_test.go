package telegrampipeline_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
)

func TestCallbackRegistrySnapshotRereadsIssuedTokenClaimsWithoutKeyMaterial(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	registry, err := telegrampipeline.OpenFileCallbackRegistry(filepath.Join(t.TempDir(), "registry.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presentation := telegrampipeline.CallbackPresentation{SessionID: domain.SessionID("session-1"), Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 10}, TokenIDs: []string{"token-a"}, ExpiresAt: now.Add(time.Hour)}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Claim(context.Background(), telegrampipeline.CallbackClaim{SessionID: presentation.SessionID, Carrier: presentation.Carrier, TokenID: "token-a", ExpiresAt: presentation.ExpiresAt, UpdateID: 11, CallbackQueryID: "query-11"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Presentations[presentation.SessionID]
	if !got.Tokens["token-a"] || got.Claims["token-a"].UpdateID != 11 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}
