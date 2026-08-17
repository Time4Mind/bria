package telegramapp

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const slowTelegramOperation = 750 * time.Millisecond
const tracedTelegramOperation = 25 * time.Millisecond

// activityMessenger records only message ordering information. It never reads
// message contents and exists so a compact service log is edited only while it
// is still the newest message in the private chat.
type activityMessenger struct {
	inner  Messenger
	mu     sync.Mutex
	latest map[int64]int64
}

func newActivityMessenger(inner Messenger) *activityMessenger {
	return &activityMessenger{inner: inner, latest: make(map[int64]int64)}
}

func (m *activityMessenger) AnswerCallbackQuery(
	ctx context.Context, callbackID string, text string,
) error {
	startedAt := time.Now()
	err := m.inner.AnswerCallbackQuery(ctx, callbackID, text)
	logSlowTelegramOperation("answer_callback", startedAt, err)
	return err
}

func (m *activityMessenger) SendTyping(ctx context.Context, chatID int64) error {
	return m.inner.SendTyping(ctx, chatID)
}

func (m *activityMessenger) SendDocument(
	ctx context.Context, request telegrambot.DocumentRequest,
) (telegrambot.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	message, err := m.inner.SendDocument(ctx, request)
	if err == nil {
		m.observeLocked(message.ChatID, message.MessageID)
	}
	return message, err
}

func (m *activityMessenger) SendScreen(
	ctx context.Context, chatID int64, screen telegramui.Screen,
) (telegrambot.Message, error) {
	queuedAt := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	logSlowTelegramOperation("send_screen_queue", queuedAt, nil)
	startedAt := time.Now()
	message, err := m.inner.SendScreen(ctx, chatID, screen)
	logSlowTelegramOperation(screenOperation("send_screen", screen), startedAt, err)
	if err == nil {
		m.observeLocked(message.ChatID, message.MessageID)
	}
	return message, err
}

func (m *activityMessenger) EditScreen(
	ctx context.Context, message telegrambot.Message, screen telegramui.Screen,
) (telegrambot.Message, error) {
	queuedAt := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	logSlowTelegramOperation("edit_screen_queue", queuedAt, nil)
	startedAt := time.Now()
	edited, err := m.inner.EditScreen(ctx, message, screen)
	logSlowTelegramOperation(screenOperation("edit_screen", screen), startedAt, err)
	return edited, err
}

func screenOperation(base string, screen telegramui.Screen) string {
	if screen.Pane == nil {
		return base + "_text"
	}
	if len(screen.Pane.PNG) > 0 {
		return base + "_upload"
	}
	if screen.Pane.FileID != "" {
		return base + "_file_id"
	}
	return base + "_pane"
}

func logSlowTelegramOperation(operation string, startedAt time.Time, err error) {
	duration := time.Since(startedAt)
	if duration < tracedTelegramOperation {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	log.Printf("bria telegram: outbound_timing operation=%s duration_ms=%d outcome=%s",
		operation, duration.Milliseconds(), outcome)
	if duration < slowTelegramOperation {
		return
	}
	log.Printf("bria telegram: slow_outbound operation=%s duration_ms=%d outcome=%s",
		operation, duration.Milliseconds(), outcome)
}

func (m *activityMessenger) DeleteMessage(
	ctx context.Context, message telegrambot.Message,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.inner.DeleteMessage(ctx, message)
	if err == nil && m.latest[message.ChatID] == message.MessageID {
		// Telegram does not expose the message now revealed underneath. Force the
		// next cluster event to start a new log instead of editing an older one.
		delete(m.latest, message.ChatID)
	}
	return err
}

func (m *activityMessenger) ClearKeyboard(
	ctx context.Context, message telegrambot.Message,
) error {
	return m.inner.ClearKeyboard(ctx, message)
}

func (m *activityMessenger) observeIncoming(chatID, messageID int64) {
	if chatID <= 0 || messageID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observeLocked(chatID, messageID)
}

func (m *activityMessenger) observeLocked(chatID, messageID int64) {
	if chatID > 0 && messageID > m.latest[chatID] {
		m.latest[chatID] = messageID
	}
}

// upsertNewest serializes the last-message check with every outbound send.
// Editing does not change Telegram ordering, while a new send becomes latest.
func (m *activityMessenger) upsertNewest(
	ctx context.Context,
	chatID int64,
	previous telegrambot.Message,
	editScreen telegramui.Screen,
	newScreen telegramui.Screen,
) (telegrambot.Message, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous.ChatID == chatID && previous.MessageID > 0 &&
		m.latest[chatID] == previous.MessageID {
		edited, err := m.inner.EditScreen(ctx, previous, editScreen)
		return edited, true, err
	}
	message, err := m.inner.SendScreen(ctx, chatID, newScreen)
	if err == nil {
		m.observeLocked(message.ChatID, message.MessageID)
	}
	return message, false, err
}

var _ Messenger = (*activityMessenger)(nil)
