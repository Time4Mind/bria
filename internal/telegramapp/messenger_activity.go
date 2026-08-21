package telegramapp

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const slowTelegramOperation = 750 * time.Millisecond
const tracedTelegramOperation = 25 * time.Millisecond

// activityMessenger records only message ordering information. It never reads
// message contents and exists so a compact service log is edited only while it
// is still the newest message in the private chat.
type activityMessenger struct {
	inner             Messenger
	mu                sync.Mutex
	latest            map[int64]int64
	outboundBlockedAt map[int64]time.Time
}

func newActivityMessenger(inner Messenger) *activityMessenger {
	return &activityMessenger{
		inner: inner, latest: make(map[int64]int64),
		outboundBlockedAt: make(map[int64]time.Time),
	}
}

func (m *activityMessenger) AnswerCallbackQuery(
	ctx context.Context, callbackID string, text string,
) error {
	startedAt := time.Now()
	err := m.inner.AnswerCallbackQuery(ctx, callbackID, text)
	logSlowTelegramOperation("answer_callback", 0, startedAt, err)
	return err
}

func (m *activityMessenger) SendTyping(ctx context.Context, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.localFloodWaitLocked("sendChatAction", chatID); err != nil {
		return err
	}
	err := m.inner.SendTyping(ctx, chatID)
	m.rememberFloodWaitLocked(chatID, err)
	return err
}

func (m *activityMessenger) SendDocument(
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

func (m *activityMessenger) SendScreen(
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

func (m *activityMessenger) EditScreen(
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

func (m *activityMessenger) editFloodWait(chatID int64) (time.Duration, bool) {
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

func (m *activityMessenger) localFloodWaitLocked(method string, chatID int64) error {
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

func (m *activityMessenger) rememberFloodWaitLocked(chatID int64, err error) {
	retryAfter, limited := telegrambot.RemoteFloodWait(err)
	if !limited || retryAfter <= 0 {
		return
	}
	blockedAt := time.Now().Add(retryAfter)
	if !blockedAt.After(m.outboundBlockedAt[chatID]) {
		return
	}
	m.outboundBlockedAt[chatID] = blockedAt
	processlog.Servicef("bria telegram: outbound_flood_wait retry_after_ms=%d error=%v",
		retryAfter.Milliseconds(), err)
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

func logSlowTelegramOperation(
	operation string,
	messageID int64,
	startedAt time.Time,
	err error,
) {
	logSlowTelegramOperationContext(
		context.Background(), operation, messageID, startedAt, err,
	)
}

func logSlowTelegramOperationContext(
	ctx context.Context,
	operation string,
	messageID int64,
	startedAt time.Time,
	err error,
) {
	duration := time.Since(startedAt)
	if duration < tracedTelegramOperation {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	restoreFields := ""
	if tag, ok := restoreTimingFromContext(ctx); ok {
		restoreFields = " restore_ref=" + strconv.Quote(tag.ref.Key()) + " restore_generation=" +
			strconv.FormatUint(tag.generation, 10) + " restore_stage=" + tag.stage
	}
	processlog.Detailf(
		"bria telegram: outbound_timing operation=%s message_id=%d duration_ms=%d outcome=%s%s",
		operation, messageID, duration.Milliseconds(), outcome, restoreFields,
	)
	if duration < slowTelegramOperation {
		return
	}
	processlog.Servicef(
		"bria telegram: slow_outbound operation=%s message_id=%d duration_ms=%d outcome=%s%s",
		operation, messageID, duration.Milliseconds(), outcome, restoreFields,
	)
}

func (m *activityMessenger) DeleteMessage(
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

func (m *activityMessenger) ClearKeyboard(
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

func (m *activityMessenger) ReplaceKeyboard(
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

var _ Messenger = (*activityMessenger)(nil)
