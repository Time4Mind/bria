package telegramapp

import (
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestTelegramMessageRestoresScreenFingerprint(t *testing.T) {
	card := domain.TelegramResponseCard{
		ChatID: 7, MessageID: 91, Rich: true,
		RichMediaFileID: "photo", PaneHash: "pane", ScreenHash: "screen",
	}
	message := telegramMessage(card)
	if message.ChatID != card.ChatID || message.MessageID != card.MessageID ||
		message.Rich != card.Rich || message.RichMediaFileID != card.RichMediaFileID ||
		message.PaneHash != card.PaneHash || message.ScreenHash != card.ScreenHash {
		t.Fatalf("restored Telegram message = %#v want metadata from %#v", message, card)
	}
}
