package telegramapp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestSuccessfulNodeRenameReturnsDirectlyToNodeSettings(t *testing.T) {
	fixture := newFixture(t)
	token, err := fixture.codec.Node(7, telegramui.ActionNodeRename, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	rename := encodeCallback(t, telegramui.ActionNodeRename, token)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 71, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "rename", CallbackData: rename,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 19},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 72, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "Renamed server",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("rename result screens=%#v", fixture.messenger.sent)
	}
	screen := fixture.messenger.sent[0]
	if screen.Name != telegramui.ScreenStatus ||
		!strings.Contains(screen.Text, "Renamed server") || len(screen.Grid) < 2 {
		t.Fatalf("rename did not return full node settings: %#v", screen)
	}
	if strings.Contains(screen.Text, "Node renamed") {
		t.Fatalf("rename left an intermediate result menu: %#v", screen)
	}
}
