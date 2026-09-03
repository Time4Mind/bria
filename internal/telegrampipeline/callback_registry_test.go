package telegrampipeline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
)

func TestFileCallbackRegistryPersistsClaimsAndLatestPresentation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	path := filepath.Join(t.TempDir(), "callback-registry.json")
	registry, err := telegrampipeline.OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presentation := telegrampipeline.CallbackPresentation{
		SessionID: domain.SessionID("123e4567-e89b-12d3-a456-426614174000"),
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 99},
		TokenIDs:  []string{"token-a", "token-b"},
		ExpiresAt: now.Add(time.Minute),
		AcceptedTurnRecovery: &telegrampipeline.AcceptedTurnRecoveryBinding{
			SessionID: "123e4567-e89b-12d3-a456-426614174000", MessageID: "telegram-update:301", BindingGeneration: 7,
		},
	}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	claim := telegrampipeline.CallbackClaim{
		SessionID:       presentation.SessionID,
		Carrier:         presentation.Carrier,
		TokenID:         "token-a",
		ExpiresAt:       presentation.ExpiresAt,
		UpdateID:        10,
		CallbackQueryID: "query-a",
	}
	result, err := registry.Claim(context.Background(), claim)
	if err != nil || result.Outcome != telegrampipeline.ClaimAccepted {
		t.Fatalf("first claim = %#v, %v", result, err)
	}

	reopened, err := telegrampipeline.OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err = reopened.Claim(context.Background(), claim)
	if err != nil || result.Outcome != telegrampipeline.ClaimRecovered {
		t.Fatalf("reopened claim = %#v, %v, want recovered exact delivery", result, err)
	}
	if result.AcceptedTurnRecovery == nil || result.AcceptedTurnRecovery.MessageID != "telegram-update:301" ||
		result.AcceptedTurnRecovery.BindingGeneration != 7 {
		t.Fatalf("reopened accepted-turn binding = %#v", result.AcceptedTurnRecovery)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("registry permissions = %v, %v, want 0600", info, err)
	}

	replacement := presentation
	replacement.TokenIDs = []string{"token-c"}
	replacement.Carrier.MessageID = 100
	if err := reopened.Replace(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	stale, err := reopened.Claim(context.Background(), claim)
	if err != nil || stale.Outcome != telegrampipeline.ClaimStale {
		t.Fatalf("replaced presentation old claim = %#v, %v, want stale", stale, err)
	}
}

func TestFileCallbackRegistryInvalidatesExactCarrierDurably(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	path := filepath.Join(t.TempDir(), "callback-registry.json")
	registry, err := telegrampipeline.OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presentation := telegrampipeline.CallbackPresentation{
		SessionID: "123e4567-e89b-12d3-a456-426614174000", Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99},
		TokenIDs: []string{"terminal-token"}, ExpiresAt: now.Add(time.Minute), InteractionRequestID: "request-1",
	}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	if err := registry.InvalidateCarrier(context.Background(), presentation.Carrier); err != nil {
		t.Fatal(err)
	}
	reopened, err := telegrampipeline.OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Claim(context.Background(), telegrampipeline.CallbackClaim{
		SessionID: presentation.SessionID, Carrier: presentation.Carrier, TokenID: "terminal-token",
		ExpiresAt: presentation.ExpiresAt, UpdateID: 1, CallbackQueryID: "query",
	})
	if err != nil || result.Outcome != telegrampipeline.ClaimStale {
		t.Fatalf("claim after durable invalidation = %#v, %v", result, err)
	}
}

func TestFileCallbackRegistryRejectsCorruptStateWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "callback-registry.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"presentations":{}} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := telegrampipeline.OpenFileCallbackRegistry(path, time.Now); err == nil {
		t.Fatal("OpenFileCallbackRegistry() accepted corrupt state")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"version":1,"presentations":{}} trailing` {
		t.Fatal("corrupt state was overwritten")
	}
}

func TestFileCallbackRegistryDoesNotAcceptClaimWhenPersistenceFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission failure is not reliable as root")
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	dir := t.TempDir()
	path := filepath.Join(dir, "callback-registry.json")
	registry, err := telegrampipeline.OpenFileCallbackRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presentation := telegrampipeline.CallbackPresentation{
		SessionID: "123e4567-e89b-12d3-a456-426614174000",
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 99},
		TokenIDs:  []string{"token"},
		ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	claim := telegrampipeline.CallbackClaim{
		SessionID:       presentation.SessionID,
		Carrier:         presentation.Carrier,
		TokenID:         "token",
		ExpiresAt:       presentation.ExpiresAt,
		UpdateID:        1,
		CallbackQueryID: "query",
	}
	_, err = registry.Claim(context.Background(), claim)
	if err == nil {
		t.Fatal("Claim() succeeded without durable persistence")
	}
	if errors.Is(err, telegrampipeline.ErrReplayedCallback) {
		t.Fatalf("Claim() returned semantic replay instead of persistence error: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Claim(context.Background(), claim)
	if err != nil || result.Outcome != telegrampipeline.ClaimAccepted {
		t.Fatalf("claim after restoring persistence = %#v, %v", result, err)
	}
}
