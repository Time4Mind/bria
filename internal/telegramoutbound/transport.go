package telegramoutbound

import (
	"context"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

// Transport is the outbound Telegram capability consumed by Coordinator.
type Transport interface {
	AnswerCallbackQuery(context.Context, string, string) error
	SendTyping(context.Context, int64) error
	SendDocument(context.Context, telegrambot.DocumentRequest) (telegrambot.Message, error)
	SendScreen(context.Context, int64, telegramui.Screen) (telegrambot.Message, error)
	EditScreen(context.Context, telegrambot.Message, telegramui.Screen) (telegrambot.Message, error)
	DeleteMessage(context.Context, telegrambot.Message) error
	ClearKeyboard(context.Context, telegrambot.Message) error
}
