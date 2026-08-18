package telegramapp

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type floodEditMessenger struct {
	edits   int
	sends   int
	sendErr error
}

func (*floodEditMessenger) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*floodEditMessenger) SendTyping(context.Context, int64) error                   { return nil }
func (*floodEditMessenger) SendDocument(context.Context, telegrambot.DocumentRequest) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (m *floodEditMessenger) SendScreen(context.Context, int64, telegramui.Screen) (telegrambot.Message, error) {
	m.sends++
	return telegrambot.Message{}, m.sendErr
}
func (m *floodEditMessenger) EditScreen(context.Context, telegrambot.Message, telegramui.Screen) (telegrambot.Message, error) {
	m.edits++
	return telegrambot.Message{}, &telegrambot.APIError{
		Method: "editMessageText", Code: 429, RetryAfter: time.Minute,
	}
}
func (*floodEditMessenger) DeleteMessage(context.Context, telegrambot.Message) error { return nil }
func (*floodEditMessenger) ClearKeyboard(context.Context, telegrambot.Message) error { return nil }

func TestActivityMessengerSuppressesEditsDuringFloodWait(t *testing.T) {
	inner := &floodEditMessenger{}
	messenger := newActivityMessenger(inner)
	message := telegrambot.Message{ChatID: 7, MessageID: 9}
	screen := telegramui.Screen{Name: telegramui.ScreenMenu, Text: "menu"}
	if _, err := messenger.EditScreen(context.Background(), message, screen); err == nil {
		t.Fatal("first edit unexpectedly succeeded")
	}
	if _, err := messenger.EditScreen(context.Background(), message, screen); err == nil {
		t.Fatal("suppressed edit unexpectedly succeeded")
	}
	if inner.edits != 1 {
		t.Fatalf("inner edits=%d want=1", inner.edits)
	}
	if _, err := messenger.SendScreen(context.Background(), 7, screen); err == nil {
		t.Fatal("screen send bypassed the edit flood wait")
	}
	if inner.sends != 0 {
		t.Fatalf("inner sends=%d want=0", inner.sends)
	}
}

func TestActivityMessengerSuppressesWritesAfterSendFloodWait(t *testing.T) {
	inner := &floodEditMessenger{sendErr: &telegrambot.APIError{
		Method: "sendRichMessage", Code: 429, RetryAfter: time.Minute,
	}}
	messenger := newActivityMessenger(inner)
	screen := telegramui.Screen{Name: telegramui.ScreenMenu, Text: "menu"}
	if _, err := messenger.SendScreen(context.Background(), 7, screen); err == nil {
		t.Fatal("first send unexpectedly succeeded")
	}
	if _, err := messenger.SendScreen(context.Background(), 7, screen); err == nil {
		t.Fatal("suppressed send unexpectedly succeeded")
	}
	if inner.sends != 1 {
		t.Fatalf("inner sends=%d want=1", inner.sends)
	}
}
