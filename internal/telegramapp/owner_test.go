package telegramapp_test

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/telegrambot"
)

func TestUnknownOwnerCallbackIsAcknowledgedWithoutProjection(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 3, Kind: telegrambot.IncomingCallback, ChatID: 99, UserID: 99,
		CallbackID: "unknown", CallbackData: "menu",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.answers) != 1 || fixture.messenger.answers[0] != "unknown:" {
		t.Fatalf("unknown callback answers=%q", fixture.messenger.answers)
	}
	if len(fixture.messenger.edited) != 0 || len(fixture.messenger.sent) != 0 {
		t.Fatal("unknown callback projected a screen")
	}
}
