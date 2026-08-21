package telegramapp

import (
	"context"
	"sync"
	"testing"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type countingCardMessenger struct {
	mu    sync.Mutex
	edits int
}

func (m *countingCardMessenger) AnswerCallbackQuery(context.Context, string, string) error {
	return nil
}
func (m *countingCardMessenger) SendTyping(context.Context, int64) error { return nil }
func (m *countingCardMessenger) SendDocument(
	context.Context, telegrambot.DocumentRequest,
) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (m *countingCardMessenger) SendScreen(
	context.Context, int64, telegramui.Screen,
) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (m *countingCardMessenger) EditScreen(
	_ context.Context, origin telegrambot.Message, _ telegramui.Screen,
) (telegrambot.Message, error) {
	m.mu.Lock()
	m.edits++
	m.mu.Unlock()
	return origin, nil
}
func (m *countingCardMessenger) DeleteMessage(context.Context, telegrambot.Message) error {
	return nil
}
func (m *countingCardMessenger) ClearKeyboard(context.Context, telegrambot.Message) error {
	return nil
}
func (m *countingCardMessenger) editCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.edits
}

func TestCardTransportCoalescesSameConcurrentProjection(t *testing.T) {
	messenger := &countingCardMessenger{}
	handler := &Handler{messenger: messenger, cardTransports: make(map[string]telegrambot.Message)}
	origin := telegrambot.Message{ChatID: 7, MessageID: 11, Rich: true}
	screen := telegramui.Screen{Text: "same", Pane: &telegramui.PaneImage{Hash: "pane"}}

	handler.cardEditMu.Lock()
	first, err := handler.editCardTransportLocked(context.Background(), origin, screen)
	handler.cardEditMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	handler.cardEditMu.Lock()
	second, err := handler.editCardTransportLocked(context.Background(), origin, screen)
	handler.cardEditMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := messenger.editCount(); got != 1 {
		t.Fatalf("edits=%d, want one coalesced edit", got)
	}
	if first.ScreenHash == "" || second.ScreenHash != first.ScreenHash {
		t.Fatalf("transport fingerprints first=%q second=%q", first.ScreenHash, second.ScreenHash)
	}
}
