package telegramapp_test

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestMenuFromRichCardKeepsExistingCarrier(t *testing.T) {
	fixture := newFixture(t)
	data, err := (telegramui.Callback{Action: telegramui.ActionMenu}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 4, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "menu", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("rich carrier was unnecessarily replaced: %#v", fixture.messenger.sent)
	}
	if len(fixture.messenger.edited) != 1 ||
		fixture.messenger.edited[0].Name != telegramui.ScreenMenu {
		t.Fatalf("edited=%#v", fixture.messenger.edited)
	}
	if len(fixture.messenger.deleted) != 0 {
		t.Fatalf("rich carrier was deleted: %#v", fixture.messenger.deleted)
	}
}
