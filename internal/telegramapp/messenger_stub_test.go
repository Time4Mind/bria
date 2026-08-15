package telegramapp_test

import (
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func stubMessageForScreen(index int, screen telegramui.Screen) telegrambot.Message {
	message := telegrambot.Message{
		ChatID: 7, MessageID: int64(index),
		Rich: screen.RichMarkdown || screen.Pane != nil,
	}
	if screen.Pane != nil {
		message.PaneHash = screen.Pane.Hash
	}
	return message
}
