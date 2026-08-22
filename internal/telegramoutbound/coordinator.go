// Package telegramoutbound coordinates bounded Telegram Bot API writes.
// It owns transport serialization, flood-wait suppression, message ordering,
// and outbound timing, but no response-card or visible-session semantics.
package telegramoutbound

import (
	"context"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

// Coordinator records only message ordering information. It never reads
// message contents and exists so a compact service log is edited only while it
// is still the newest message in the private chat.
type Coordinator struct {
	inner             Transport
	mu                sync.Mutex
	latest            map[int64]int64
	outboundBlockedAt map[int64]time.Time
}

func New(inner Transport) *Coordinator {
	return &Coordinator{
		inner: inner, latest: make(map[int64]int64),
		outboundBlockedAt: make(map[int64]time.Time),
	}
}

func (m *Coordinator) AnswerCallbackQuery(
	ctx context.Context, callbackID string, text string,
) error {
	startedAt := time.Now()
	err := m.inner.AnswerCallbackQuery(ctx, callbackID, text)
	logSlowTelegramOperation("answer_callback", 0, startedAt, err)
	return err
}

func (m *Coordinator) SendTyping(ctx context.Context, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.localFloodWaitLocked("sendChatAction", chatID); err != nil {
		return err
	}
	err := m.inner.SendTyping(ctx, chatID)
	m.rememberFloodWaitLocked(chatID, err)
	return err
}

func (m *Coordinator) SendDocument(
	ctx context.Context, request telegrambot.DocumentRequest,
) (telegrambot.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.localFloodWaitLocked("sendDocument", request.ChatID); err != nil {
		return telegrambot.Message{}, err
	}
	message, err := m.inner.SendDocument(ctx, request)
	m.rememberFloodWaitLocked(request.ChatID, err)
	if err == nil {
		m.observeLocked(message.ChatID, message.MessageID)
	}
	return message, err
}

func (m *Coordinator) SendScreen(
	ctx context.Context, chatID int64, screen telegramui.Screen,
) (telegrambot.Message, error) {
	queuedAt := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	logSlowTelegramOperation("send_screen_queue", 0, queuedAt, nil)
	if err := m.localFloodWaitLocked("sendScreen", chatID); err != nil {
		return telegrambot.Message{}, err
	}
	startedAt := time.Now()
	message, err := m.inner.SendScreen(ctx, chatID, screen)
	logSlowTelegramOperation(
		screenOperation("send_screen", screen), message.MessageID, startedAt, err,
	)
	m.rememberFloodWaitLocked(chatID, err)
	if err == nil {
		m.observeLocked(message.ChatID, message.MessageID)
	}
	return message, err
}

func (m *Coordinator) EditScreen(
	ctx context.Context, message telegrambot.Message, screen telegramui.Screen,
) (telegrambot.Message, error) {
	queuedAt := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	logSlowTelegramOperationContext(
		ctx, "edit_screen_queue", message.MessageID, queuedAt, nil,
	)
	if err := m.localFloodWaitLocked("editScreen", message.ChatID); err != nil {
		return telegrambot.Message{}, err
	}
	startedAt := time.Now()
	edited, err := m.inner.EditScreen(ctx, message, screen)
	logSlowTelegramOperationContext(
		ctx,
		screenOperation("edit_screen", screen), message.MessageID, startedAt, err,
	)
	m.rememberFloodWaitLocked(message.ChatID, err)
	return edited, err
}

func (m *Coordinator) EditFloodWait(chatID int64) (time.Duration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	blockedAt := m.outboundBlockedAt[chatID]
	remaining := time.Until(blockedAt)
	if remaining <= 0 {
		delete(m.outboundBlockedAt, chatID)
		return 0, false
	}
	return remaining, true
}

func (m *Coordinator) localFloodWaitLocked(method string, chatID int64) error {
	blockedAt := m.outboundBlockedAt[chatID]
	if !time.Now().Before(blockedAt) {
		delete(m.outboundBlockedAt, chatID)
		return nil
	}
	return &telegrambot.APIError{
		Method: method, Code: 429, Description: "local flood wait",
		RetryAfter: time.Until(blockedAt), Local: true,
	}
}

func (m *Coordinator) rememberFloodWaitLocked(chatID int64, err error) {
	retryAfter, limited := telegrambot.RemoteFloodWait(err)
	if !limited || retryAfter <= 0 {
		return
	}
	blockedAt := time.Now().Add(retryAfter)
	if !blockedAt.After(m.outboundBlockedAt[chatID]) {
		return
	}
	m.outboundBlockedAt[chatID] = blockedAt
	processlog.Failuref(
		processlog.Service, processlog.FailureRateLimited,
		"bria telegram: outbound_flood_wait retry_after_ms=%d outcome=rate_limited",
		retryAfter.Milliseconds(),
	)
}

func (m *Coordinator) DeleteMessage(
	ctx context.Context, message telegrambot.Message,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.localFloodWaitLocked("deleteMessage", message.ChatID); err != nil {
		return err
	}
	err := m.inner.DeleteMessage(ctx, message)
	m.rememberFloodWaitLocked(message.ChatID, err)
	if err == nil && m.latest[message.ChatID] == message.MessageID {
		// Telegram does not expose the message now revealed underneath. Force the
		// next cluster event to start a new log instead of editing an older one.
		delete(m.latest, message.ChatID)
	}
	return err
}

func (m *Coordinator) ClearKeyboard(
	ctx context.Context, message telegrambot.Message,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.localFloodWaitLocked("clearKeyboard", message.ChatID); err != nil {
		return err
	}
	err := m.inner.ClearKeyboard(ctx, message)
	m.rememberFloodWaitLocked(message.ChatID, err)
	return err
}

func (m *Coordinator) ReplaceKeyboard(
	ctx context.Context,
	message telegrambot.Message,
	grid telegramui.Grid,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.localFloodWaitLocked("replaceKeyboard", message.ChatID); err != nil {
		return err
	}
	replacer, ok := m.inner.(interface {
		ReplaceKeyboard(context.Context, telegrambot.Message, telegramui.Grid) error
	})
	var err error
	if ok {
		err = replacer.ReplaceKeyboard(ctx, message, grid)
	} else {
		err = m.inner.ClearKeyboard(ctx, message)
	}
	m.rememberFloodWaitLocked(message.ChatID, err)
	return err
}

func (m *Coordinator) ObserveIncoming(chatID, messageID int64) {
	if chatID <= 0 || messageID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observeLocked(chatID, messageID)
}

func (m *Coordinator) observeLocked(chatID, messageID int64) {
	if chatID > 0 && messageID > m.latest[chatID] {
		m.latest[chatID] = messageID
	}
}

// UpsertNewest serializes the last-message check with every outbound send.
// Editing does not change Telegram ordering, while a new send becomes latest.
func (m *Coordinator) UpsertNewest(
	ctx context.Context,
	chatID int64,
	previous telegrambot.Message,
	editScreen telegramui.Screen,
	newScreen telegramui.Screen,
) (telegrambot.Message, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.localFloodWaitLocked("upsertScreen", chatID); err != nil {
		return telegrambot.Message{}, false, err
	}
	if previous.ChatID == chatID && previous.MessageID > 0 &&
		m.latest[chatID] == previous.MessageID {
		edited, err := m.inner.EditScreen(ctx, previous, editScreen)
		m.rememberFloodWaitLocked(chatID, err)
		return edited, true, err
	}
	message, err := m.inner.SendScreen(ctx, chatID, newScreen)
	m.rememberFloodWaitLocked(chatID, err)
	if err == nil {
		m.observeLocked(message.ChatID, message.MessageID)
	}
	return message, false, err
}

var _ Transport = (*Coordinator)(nil)
