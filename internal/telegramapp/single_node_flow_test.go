package telegramapp_test

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestSessionsSkipsServerPickerWhenOnlyOneEnabledNodeExists(t *testing.T) {
	fixture := newFixture(t)
	data := encodeCallback(t, telegramui.ActionSessions, "")
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 401, Kind: telegrambot.IncomingCallback,
		UserID: 7, ChatID: 7, CallbackID: "sessions", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 90},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 ||
		fixture.messenger.edited[0].Name != telegramui.ScreenSessionCard {
		t.Fatalf("screen=%#v", fixture.messenger.edited)
	}
}
