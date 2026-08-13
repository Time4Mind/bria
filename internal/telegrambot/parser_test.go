package telegrambot

import (
	"errors"
	"strings"
	"testing"
)

func TestParsePrivateDMMessageAndCallback(t *testing.T) {
	message, err := ParsePrivateDM(Update{
		UpdateID: 1,
		Message: &UpdateMessage{
			MessageID: 4, FromID: 42, LanguageCode: "ru-RU", ChatID: 42,
			ChatType: "private", Text: "hello",
		},
	})
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	if message.Kind != IncomingMessage || message.UserID != 42 || message.Text != "hello" ||
		message.LanguageCode != "ru-RU" {
		t.Fatalf("unexpected message event: %#v", message)
	}

	callback, err := ParsePrivateDM(Update{
		UpdateID: 2,
		CallbackQuery: &CallbackQuery{
			ID: "callback-1", FromID: 42, FromLanguageCode: "zh-CN", Data: "session:opaque",
			// Telegram's message.from is the bot, not the user pressing the button.
			Message: &UpdateMessage{
				MessageID: 5, FromID: 999, ChatID: 42, ChatType: "private",
				RichMediaFileID: "pane-photo", RichMessage: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("parse callback: %v", err)
	}
	if callback.Kind != IncomingCallback || callback.CallbackID != "callback-1" ||
		callback.LanguageCode != "zh-CN" ||
		callback.CallbackOrigin != (Message{
			ChatID: 42, MessageID: 5, Rich: true, RichMediaFileID: "pane-photo",
		}) {
		t.Fatalf("unexpected callback event: %#v", callback)
	}
}

func TestParsePrivateDMFailsClosed(t *testing.T) {
	tests := []Update{
		{},
		{UpdateID: 1, Message: &UpdateMessage{MessageID: 1, FromID: 42, ChatID: -1, ChatType: "group"}},
		{UpdateID: 1, Message: &UpdateMessage{MessageID: 1, FromID: 42, ChatID: 7, ChatType: "private"}},
		{UpdateID: 1, CallbackQuery: &CallbackQuery{
			ID: "id", FromID: 42, Data: "menu",
			Message: &UpdateMessage{MessageID: 1, ChatID: 7, ChatType: "private"},
		}},
		{UpdateID: 1, CallbackQuery: &CallbackQuery{
			ID: "id", FromID: 42, Data: strings.Repeat("x", 65),
			Message: &UpdateMessage{MessageID: 1, ChatID: 42, ChatType: "private"},
		}},
		{UpdateID: 1,
			Message:       &UpdateMessage{MessageID: 1, FromID: 42, ChatID: 42, ChatType: "private"},
			CallbackQuery: &CallbackQuery{ID: "id"},
		},
	}
	for index, update := range tests {
		if _, err := ParsePrivateDM(update); !errors.Is(err, ErrNotPrivateDM) {
			t.Fatalf("case %d returned %v, want ErrNotPrivateDM", index, err)
		}
	}
}
