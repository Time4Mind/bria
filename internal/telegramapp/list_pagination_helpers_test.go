package telegramapp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Time4Mind/bria/internal/telegrambot"
)

func invokeListCallback(
	t *testing.T,
	fixture fixture,
	updateID int64,
	data string,
	origin telegrambot.Message,
) {
	t.Helper()
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: updateID, Kind: telegrambot.IncomingCallback,
		UserID: 7, ChatID: 7, CallbackID: fmt.Sprintf("page-%d", updateID),
		CallbackData: data, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
}
