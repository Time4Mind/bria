package telegrambridge

import (
	"bria/internal/coordinator"
	"bria/internal/telegram"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	pollLimit          = 100
	pollTimeoutSeconds = 20
	defaultRetryDelay  = time.Second
	maxRetryDelay      = 30 * time.Second
)

var allowedUpdates = []string{"message", "callback_query"}

type Source struct {
	client     *telegram.Client
	retryDelay time.Duration
}

var _ coordinator.Source = (*Source)(nil)

func NewSource(client *telegram.Client) (*Source, error) {
	return NewSourceWithOptions(client, RetryOptions{})
}

type RetryOptions struct {
	Delay time.Duration
}

func NewSourceWithOptions(
	client *telegram.Client,
	options RetryOptions,
) (*Source, error) {
	if client == nil {
		return nil, errors.New("Telegram client is required")
	}
	retryDelay, err := normalizeRetryDelay(options)
	if err != nil {
		return nil, err
	}
	return &Source{client: client, retryDelay: retryDelay}, nil
}

func (source *Source) Bootstrap(ctx context.Context) (int64, error) {
	var fence telegram.UpdateID
	retryDelay := source.retryDelay
	for {
		var err error
		fence, err = source.client.BootstrapOffset(ctx, telegram.BootstrapOffsetRequest{
			ForgetPendingUpdates: true,
		})
		if err == nil {
			break
		}
		if !telegram.IsTransient(err) {
			return 0, fmt.Errorf("bootstrap Telegram updates: %w", err)
		}
		if err := waitForRetry(ctx, retryDelay); err != nil {
			return 0, err
		}
		retryDelay = nextRetryDelay(retryDelay)
	}
	if fence == 0 {
		return 1, nil
	}
	return int64(fence), nil
}
func (source *Source) Poll(ctx context.Context, nextUpdateID int64) ([]coordinator.Update, error) {
	if nextUpdateID <= 0 {
		return nil, errors.New("next Telegram update id must be positive")
	}
	var updates []telegram.Update
	retryDelay := source.retryDelay
	for {
		var err error
		updates, err = source.client.GetUpdates(ctx, telegram.GetUpdatesRequest{
			Offset:         telegram.UpdateID(nextUpdateID),
			Limit:          pollLimit,
			TimeoutSeconds: pollTimeoutSeconds,
			AllowedUpdates: append([]string(nil), allowedUpdates...),
		})
		if err == nil {
			break
		}
		if !telegram.IsTransient(err) {
			return nil, fmt.Errorf("poll Telegram updates: %w", err)
		}
		if err := waitForRetry(ctx, retryDelay); err != nil {
			return nil, err
		}
		retryDelay = nextRetryDelay(retryDelay)
	}
	result := make([]coordinator.Update, 0, len(updates))
	for _, update := range updates {
		result = append(result, normalizeUpdate(update))
	}
	return result, nil
}
func normalizeUpdate(update telegram.Update) coordinator.Update {
	result := coordinator.Update{ID: int64(update.UpdateID)}
	if update.Skipped {
		return result
	}
	if update.Message != nil {
		result.Kind = coordinator.UpdateMessage
		result.ActorID = int64(update.Message.From.ID)
		result.ConversationID = int64(update.Message.Chat.ID)
		result.ConversationKind = update.Message.Chat.Type
		result.Text = update.Message.Text
		result.Caption = update.Message.Caption
		result.SourceMessageID = int64(update.Message.MessageID)
		if update.Message.ReplyToMessage != nil {
			result.ReplyToMessageID = int64(update.Message.ReplyToMessage.MessageID)
		}
		switch {
		case update.Message.Voice != nil:
			voice := update.Message.Voice
			result.MediaKind = string(telegram.MediaVoice)
			result.MediaFileID = voice.FileID
			result.MediaFileUniqueID = voice.FileUniqueID
			result.MediaFileSize = voice.FileSize
			result.MediaMIMEType = voice.MIMEType
			result.MediaDurationSeconds = voice.Duration
			result.MediaDownloadAllowed = true
		case len(update.Message.Photo) > 0:
			photo := largestPhoto(update.Message.Photo)
			result.MediaKind = string(telegram.MediaPhoto)
			result.MediaFileID = photo.FileID
			result.MediaFileUniqueID = photo.FileUniqueID
			result.MediaFileSize = photo.FileSize
			result.MediaWidth = photo.Width
			result.MediaHeight = photo.Height
			result.MediaDownloadAllowed = true
		case update.Message.Video != nil:
			video := update.Message.Video
			result.MediaKind = string(telegram.MediaVideo)
			result.MediaFileID = video.FileID
			result.MediaFileUniqueID = video.FileUniqueID
			result.MediaFileSize = video.FileSize
			result.MediaMIMEType = video.MIMEType
			result.MediaDurationSeconds = video.Duration
			result.MediaWidth = video.Width
			result.MediaHeight = video.Height
			result.MediaDownloadAllowed = false
		}
		return result
	}
	if update.CallbackQuery != nil {
		result.Kind = coordinator.UpdateCallback
		result.ActorID = int64(update.CallbackQuery.From.ID)
		result.ConversationID = int64(update.CallbackQuery.Message.Chat.ID)
		result.ConversationKind = update.CallbackQuery.Message.Chat.Type
		result.Text = update.CallbackQuery.Data
		result.CallbackQueryID = string(update.CallbackQuery.ID)
		result.SourceMessageID = int64(update.CallbackQuery.Message.MessageID)
	}
	return result
}
func largestPhoto(sizes []telegram.PhotoSize) telegram.PhotoSize {
	largest := sizes[0]
	for _, candidate := range sizes[1:] {
		candidateArea := int64(candidate.Width) * int64(candidate.Height)
		largestArea := int64(largest.Width) * int64(largest.Height)
		if candidateArea > largestArea ||
			(candidateArea == largestArea && candidate.FileSize > largest.FileSize) {
			largest = candidate
		}
	}
	return largest
}

type Sender struct {
	client *telegram.Client
}

var _ coordinator.Sender = (*Sender)(nil)

func NewSender(client *telegram.Client) (*Sender, error) {
	if client == nil {
		return nil, errors.New("Telegram client is required")
	}
	return &Sender{client: client}, nil
}
func (sender *Sender) SendStatus(
	ctx context.Context,
	_ string,
	status coordinator.Status,
) (coordinator.Receipt, error) {
	if status.CallbackQueryID != "" {
		if err := sender.client.AnswerCallbackQuery(ctx, telegram.AnswerCallbackQueryRequest{CallbackQueryID: telegram.CallbackQueryID(status.CallbackQueryID)}); err != nil {
			return coordinator.Receipt{}, fmt.Errorf("answer Telegram callback: %w", err)
		}
	}
	message, err := sender.client.SendMessage(ctx, telegram.SendMessageRequest{
		ChatID: telegram.ChatID(status.ConversationID),
		Text:   status.Text,
	})
	if err != nil {
		return coordinator.Receipt{}, fmt.Errorf("send Telegram status: %w", err)
	}
	if message.MessageID <= 0 {
		return coordinator.Receipt{}, errors.New("Telegram send returned a non-positive message id")
	}
	return coordinator.Receipt{MessageID: int64(message.MessageID)}, nil
}

func (sender *Sender) SendStatusWithKeyboard(
	ctx context.Context,
	_ string,
	status coordinator.Status,
	keyboard *coordinator.KeyboardMarkup,
) (coordinator.Receipt, error) {
	if keyboard == nil {
		return sender.SendStatus(ctx, "", status)
	}
	if status.CallbackQueryID != "" {
		if err := sender.client.AnswerCallbackQuery(ctx, telegram.AnswerCallbackQueryRequest{CallbackQueryID: telegram.CallbackQueryID(status.CallbackQueryID)}); err != nil {
			return coordinator.Receipt{}, fmt.Errorf("answer Telegram callback: %w", err)
		}
	}
	markup := coordinatorMarkup(keyboard)
	message, err := sender.client.SendMessage(ctx, telegram.SendMessageRequest{
		ChatID: telegram.ChatID(status.ConversationID), Text: status.Text, ReplyMarkup: markup,
	})
	if err != nil {
		return coordinator.Receipt{}, fmt.Errorf("send Telegram status with keyboard: %w", err)
	}
	if message.MessageID <= 0 {
		return coordinator.Receipt{}, errors.New("Telegram send returned a non-positive message id")
	}
	return coordinator.Receipt{MessageID: int64(message.MessageID)}, nil
}

func (sender *Sender) EditStatusWithKeyboard(
	ctx context.Context,
	_ string,
	status coordinator.Status,
	keyboard *coordinator.KeyboardMarkup,
) (coordinator.Receipt, error) {
	if status.SourceMessageID <= 0 {
		return coordinator.Receipt{}, errors.New("source message id is required for card edit")
	}
	if status.CallbackQueryID != "" {
		if err := sender.client.AnswerCallbackQuery(ctx, telegram.AnswerCallbackQueryRequest{CallbackQueryID: telegram.CallbackQueryID(status.CallbackQueryID)}); err != nil {
			return coordinator.Receipt{}, fmt.Errorf("answer Telegram callback: %w", err)
		}
	}
	markup := coordinatorMarkup(keyboard)
	message, err := sender.client.EditMessageText(ctx, telegram.EditMessageTextRequest{
		ChatID: telegram.ChatID(status.ConversationID), MessageID: telegram.MessageID(status.SourceMessageID), Text: status.Text, ReplyMarkup: markup,
	})
	if err != nil {
		var apiErr *telegram.APIError
		if errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Description), "message is not modified") {
			return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
		}
		return coordinator.Receipt{}, fmt.Errorf("edit Telegram status: %w", err)
	}
	if message.MessageID <= 0 {
		return coordinator.Receipt{}, errors.New("Telegram edit returned a non-positive message id")
	}
	return coordinator.Receipt{MessageID: int64(message.MessageID)}, nil
}
func coordinatorMarkup(keyboard *coordinator.KeyboardMarkup) *telegram.InlineKeyboardMarkup {
	if keyboard == nil {
		return nil
	}
	markup := &telegram.InlineKeyboardMarkup{InlineKeyboard: make([][]telegram.InlineKeyboardButton, len(*keyboard))}
	for rowIndex, row := range *keyboard {
		markup.InlineKeyboard[rowIndex] = make([]telegram.InlineKeyboardButton, len(row))
		for buttonIndex, button := range row {
			markup.InlineKeyboard[rowIndex][buttonIndex] = telegram.InlineKeyboardButton{Text: button.Text, CallbackData: button.CallbackData}
		}
	}
	return markup
}

type Readiness struct {
	client           *telegram.Client
	expectedUsername string
	retryDelay       time.Duration
	retryTransient   bool
}

var _ coordinator.Readiness = (*Readiness)(nil)

func NewReadiness(client *telegram.Client, expectedUsername string) (*Readiness, error) {
	return newReadiness(client, expectedUsername, RetryOptions{}, false)
}

func NewPersistentReadiness(
	client *telegram.Client,
	expectedUsername string,
) (*Readiness, error) {
	return NewPersistentReadinessWithOptions(client, expectedUsername, RetryOptions{})
}
func NewPersistentReadinessWithOptions(
	client *telegram.Client,
	expectedUsername string,
	options RetryOptions,
) (*Readiness, error) {
	return newReadiness(client, expectedUsername, options, true)
}
func newReadiness(
	client *telegram.Client,
	expectedUsername string,
	options RetryOptions,
	retryTransient bool,
) (*Readiness, error) {
	if client == nil {
		return nil, errors.New("Telegram client is required")
	}
	if !validExpectedUsername(expectedUsername) {
		return nil, errors.New("expected Telegram bot username must use exact Bot API username form without @")
	}
	retryDelay, err := normalizeRetryDelay(options)
	if err != nil {
		return nil, err
	}
	return &Readiness{
		client:           client,
		expectedUsername: expectedUsername,
		retryDelay:       retryDelay,
		retryTransient:   retryTransient,
	}, nil
}
func (readiness *Readiness) Ready(ctx context.Context, _ coordinator.Checkpoint) error {
	var identity telegram.User
	retryDelay := readiness.retryDelay
	for {
		var err error
		identity, err = readiness.client.GetMe(ctx)
		if err == nil {
			break
		}
		if !readiness.retryTransient || !telegram.IsTransient(err) {
			return fmt.Errorf("verify Telegram bot identity: %w", err)
		}
		if err := waitForRetry(ctx, retryDelay); err != nil {
			return err
		}
		retryDelay = nextRetryDelay(retryDelay)
	}
	if identity.ID <= 0 || !identity.IsBot || identity.Username != readiness.expectedUsername {
		return errors.New("Telegram credential does not match the expected bot identity")
	}
	return nil
}
func normalizeRetryDelay(options RetryOptions) (time.Duration, error) {
	if options.Delay < 0 {
		return 0, errors.New("Telegram transient retry delay must not be negative")
	}
	if options.Delay == 0 {
		return defaultRetryDelay, nil
	}
	if options.Delay > maxRetryDelay {
		return 0, errors.New("Telegram transient retry delay must not exceed thirty seconds")
	}
	return options.Delay, nil
}
func nextRetryDelay(delay time.Duration) time.Duration {
	if delay >= maxRetryDelay/2 {
		return maxRetryDelay
	}
	return delay * 2
}
func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func validExpectedUsername(username string) bool {
	if username == "" || username[0] == '@' || strings.TrimSpace(username) != username {
		return false
	}
	for _, character := range username {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' {
			continue
		}
		return false
	}
	return true
}
