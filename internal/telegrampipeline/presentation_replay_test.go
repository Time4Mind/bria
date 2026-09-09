package telegrampipeline_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
)

func TestExactPresentationReplayPreservesClaims(t *testing.T) {
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "file-reopened"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0).UTC()
			path := filepath.Join(t.TempDir(), "callbacks.json")
			var registry telegrampipeline.CallbackRegistry = telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
			if disk {
				var err error
				registry, err = telegrampipeline.OpenFileCallbackRegistry(path, func() time.Time { return now })
				if err != nil {
					t.Fatal(err)
				}
			}
			presentation := telegrampipeline.CallbackPresentation{SessionID: "123e4567-e89b-12d3-a456-426614174000", Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 5514}, TokenIDs: []string{"token-a", "token-b"}, ExpiresAt: now.Add(time.Minute)}
			if err := registry.Replace(ctx, presentation); err != nil {
				t.Fatal(err)
			}
			claim := telegrampipeline.CallbackClaim{SessionID: presentation.SessionID, Carrier: presentation.Carrier, TokenID: "token-a", ExpiresAt: presentation.ExpiresAt, UpdateID: 1, CallbackQueryID: "query-1"}
			if result, err := registry.Claim(ctx, claim); err != nil || result.Outcome != telegrampipeline.ClaimAccepted {
				t.Fatalf("first claim=%+v %v", result, err)
			}
			presentation.TokenIDs = []string{"token-b", "token-a"}
			if err := registry.Replace(ctx, presentation); err != nil {
				t.Fatal(err)
			}
			if disk {
				var err error
				registry, err = telegrampipeline.OpenFileCallbackRegistry(path, func() time.Time { return now })
				if err != nil {
					t.Fatal(err)
				}
			}
			if result, err := registry.Claim(ctx, claim); err != nil || result.Outcome != telegrampipeline.ClaimRecovered {
				t.Errorf("exact retry lost claim=%+v %v", result, err)
			}
			claim.UpdateID, claim.CallbackQueryID = 2, "query-2"
			if result, err := registry.Claim(ctx, claim); err != nil || result.Outcome != telegrampipeline.ClaimReplayed {
				t.Errorf("new update replay=%+v %v", result, err)
			}
			presentation.TokenIDs = []string{"token-c"}
			if err := registry.Replace(ctx, presentation); err != nil {
				t.Fatal(err)
			}
			claim.TokenID = "token-c"
			if result, err := registry.Claim(ctx, claim); err != nil || result.Outcome != telegrampipeline.ClaimAccepted {
				t.Fatalf("fresh presentation unavailable=%+v %v", result, err)
			}
		})
	}
}
