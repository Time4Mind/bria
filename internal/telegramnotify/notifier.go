// Package telegramnotify delivers provider turn notifications as new Telegram
// messages. It owns bounded notification copy, not session routing or cards.
package telegramnotify

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/domain"
	"bria/internal/notificationplan"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

const telegramTextLimit = 4096

type Notifier struct {
	client          *telegram.Client
	receiptRecorder ReceiptRecorder
	partReceipts    PartReceiptStore
}

// OutboundReceipt is a confirmed Telegram message receipt that can be used to
// route a later reply back to the logical session which emitted the message.
type OutboundReceipt struct {
	MessageID int64
	SessionID domain.SessionID
}

// ReceiptRecorder durably records confirmed Telegram message-to-session
// bindings. Implementations should make recording the same binding idempotent.
type ReceiptRecorder interface {
	RecordOutboundReceipt(context.Context, OutboundReceipt) error
}

type Options struct {
	ReceiptRecorder ReceiptRecorder
	PartReceipts    PartReceiptStore
}

var _ telegramcontroller.Notifier = (*Notifier)(nil)

func New(client *telegram.Client) (*Notifier, error) {
	return NewWithOptions(client, Options{})
}

func NewWithOptions(client *telegram.Client, options Options) (*Notifier, error) {
	if client == nil {
		return nil, errors.New("Telegram client is required")
	}
	return &Notifier{client: client, receiptRecorder: options.ReceiptRecorder, partReceipts: options.PartReceipts}, nil
}

// Notify sends every page exactly once in order. Any failed or ambiguous send
// stops delivery immediately; this boundary never retries Telegram writes.
func (notifier *Notifier) Notify(
	ctx context.Context,
	notification telegramcontroller.Notification,
) error {
	if notifier == nil || notifier.client == nil {
		return errors.New("Telegram notifier is required")
	}
	if _, durable := notifier.partReceipts.(pagePlanStore); durable && notification.OperationID != "" {
		_, err := notifier.Deliver(ctx, notification, notification.OperationID)
		return err
	}
	prefix, err := notificationPrefix(notification)
	if err != nil {
		return err
	}
	pages, err := notificationplan.Rich(prefix, notification.Text, telegramTextLimit)
	if err != nil {
		return errors.New("Telegram notification could not be paginated")
	}
	for index, page := range pages {
		message, sendErr := notifier.client.SendRichMessage(ctx, telegram.SendRichMessageRequest{
			ChatID: telegram.ChatID(notification.ConversationID),
			RichMessage: telegram.InputRichMessage{
				Markdown: page,
			},
		})
		if sendErr != nil {
			return fmt.Errorf(
				"send Telegram notification page %d of %d: %w",
				index+1,
				len(pages),
				sendErr,
			)
		}
		if message.MessageID <= 0 {
			return fmt.Errorf(
				"send Telegram notification page %d of %d returned no positive receipt",
				index+1,
				len(pages),
			)
		}
		if notifier.receiptRecorder != nil {
			if recordErr := notifier.receiptRecorder.RecordOutboundReceipt(ctx, OutboundReceipt{
				MessageID: int64(message.MessageID),
				SessionID: notification.SessionID,
			}); recordErr != nil {
				return fmt.Errorf(
					"record Telegram notification page %d of %d receipt: %w",
					index+1,
					len(pages),
					recordErr,
				)
			}
		}
	}
	return nil
}

func notificationKind(kind telegramcontroller.NotificationKind) (string, error) {
	switch kind {
	case telegramcontroller.NotificationCommentary:
		return "комментарий", nil
	case telegramcontroller.NotificationQuestion:
		return "вопрос", nil
	case telegramcontroller.NotificationFinal:
		return "итог", nil
	case telegramcontroller.NotificationError:
		return "ошибка", nil
	case telegramcontroller.NotificationPromptStatus:
		return "статус запроса", nil
	case telegramcontroller.NotificationNativeScreen:
		return "экран CLI", nil
	default:
		return "", errors.New("unsupported Telegram notification kind")
	}
}

func logicalSessionShortID(sessionID domain.SessionID) (string, error) {
	value := string(sessionID)
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", errors.New("logical session id must be a canonical UUID")
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", errors.New("logical session id must be a canonical UUID")
		}
	}
	return value[:8], nil
}
