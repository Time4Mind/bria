package callbackregistry_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/callbackregistry"
	"bria/internal/telegramstate"
)

func TestMemoryUsesProductionValidationAndSameChatFallback(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	registry := callbackregistry.NewMemory(func() time.Time { return now })
	presentation := callbackregistry.Presentation{
		SessionID: "123e4567-e89b-12d3-a456-426614174000",
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 99},
		TokenIDs:  []string{"token", "token"}, ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), presentation); err == nil {
		t.Fatal("memory registry accepted duplicate production token identity")
	}
	presentation.TokenIDs = []string{"token"}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Claim(context.Background(), callbackregistry.Claim{
		SessionID: presentation.SessionID,
		Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 12345},
		TokenID:   "token", ExpiresAt: presentation.ExpiresAt,
		UpdateID: 1, CallbackQueryID: "query",
	})
	if err != nil || result.Outcome != callbackregistry.Accepted {
		t.Fatalf("same-chat fallback = %#v, %v", result, err)
	}
}
