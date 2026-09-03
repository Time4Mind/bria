package telegramnotify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramui"
)

type DeliveryState string

const (
	DeliveryConfirmed DeliveryState = "confirmed"
	DeliveryFailed    DeliveryState = "failed"
	DeliveryUnknown   DeliveryState = "unknown"
)

type PartReceipt struct {
	PartID    string
	MessageID int64
}

type DeliveryReceipt struct {
	OperationID string
	State       DeliveryState
	Parts       []PartReceipt
}

// PartReceiptStore persists the confirmation set for a multi-page logical
// notification. ConfirmPart must be idempotent for the exact same binding.
type PartReceiptStore interface {
	ConfirmedParts(context.Context, string) ([]PartReceipt, error)
	ConfirmPart(context.Context, string, PartReceipt) error
	MarkPartUnknown(context.Context, string, string) error
}

type UnknownPartStore interface {
	UnknownParts(context.Context, string) ([]string, error)
}

// Deliver sends every not-yet-confirmed page at most once in this call. It
// never retries an ambiguous Telegram mutation. A later explicit manual retry
// reloads the confirmation set and skips already confirmed parts.
func (notifier *Notifier) Deliver(
	ctx context.Context,
	notification telegramcontroller.Notification,
	operationID string,
) (DeliveryReceipt, error) {
	if notifier == nil || notifier.client == nil || strings.TrimSpace(operationID) == "" ||
		operationID != strings.TrimSpace(operationID) {
		return DeliveryReceipt{}, errors.New("Telegram delivery operation id is required")
	}
	if notification.OperationID != "" && notification.OperationID != operationID {
		return DeliveryReceipt{}, errors.New("Telegram delivery operation identity conflicts with notification")
	}
	pages, prefix, err := notificationPages(notification)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	if len(pages) > 1 && notifier.partReceipts == nil {
		return DeliveryReceipt{}, errors.New("multi-page Telegram delivery requires durable part receipts")
	}
	receipt := DeliveryReceipt{OperationID: operationID, State: DeliveryConfirmed}
	confirmed := make(map[string]int64)
	unknown := make(map[string]struct{})
	if notifier.partReceipts != nil {
		stored, loadErr := notifier.partReceipts.ConfirmedParts(ctx, operationID)
		if loadErr != nil {
			return DeliveryReceipt{}, fmt.Errorf("load Telegram delivery confirmation set: %w", loadErr)
		}
		for _, part := range stored {
			if part.PartID == "" || part.MessageID <= 0 {
				return DeliveryReceipt{}, errors.New("Telegram delivery confirmation set is invalid")
			}
			confirmed[part.PartID] = part.MessageID
		}
		if unknownStore, ok := notifier.partReceipts.(UnknownPartStore); ok {
			storedUnknown, unknownErr := unknownStore.UnknownParts(ctx, operationID)
			if unknownErr != nil {
				return DeliveryReceipt{}, fmt.Errorf("load Telegram unknown part set: %w", unknownErr)
			}
			for _, partID := range storedUnknown {
				unknown[partID] = struct{}{}
			}
		}
	}
	for index, page := range pages {
		partID := deliveryPartID(operationID, index+1, len(pages))
		if messageID := confirmed[partID]; messageID > 0 {
			receipt.Parts = append(receipt.Parts, PartReceipt{PartID: partID, MessageID: messageID})
			continue
		}
		if _, unresolved := unknown[partID]; unresolved {
			receipt.State = DeliveryUnknown
			return receipt, fmt.Errorf("Telegram part %s has an unresolved ambiguous delivery", partID)
		}
		message, sendErr := notifier.client.SendMessage(ctx, telegram.SendMessageRequest{
			ChatID: telegram.ChatID(notification.ConversationID), Text: prefix + page.Content,
		})
		if sendErr != nil || message.MessageID <= 0 {
			receipt.State = deliveryFailureState(sendErr)
			if notifier.partReceipts != nil && receipt.State == DeliveryUnknown {
				_ = notifier.partReceipts.MarkPartUnknown(context.WithoutCancel(ctx), operationID, partID)
			}
			if sendErr == nil {
				sendErr = errors.New("Telegram returned no positive message receipt")
			}
			return receipt, fmt.Errorf("deliver Telegram part %s: %w", partID, sendErr)
		}
		part := PartReceipt{PartID: partID, MessageID: int64(message.MessageID)}
		if notifier.receiptRecorder != nil {
			if err := notifier.receiptRecorder.RecordOutboundReceipt(context.WithoutCancel(ctx), OutboundReceipt{
				MessageID: part.MessageID, SessionID: notification.SessionID,
			}); err != nil {
				receipt.State = DeliveryUnknown
				if notifier.partReceipts != nil {
					_ = notifier.partReceipts.MarkPartUnknown(context.WithoutCancel(ctx), operationID, partID)
				}
				return receipt, fmt.Errorf("record Telegram reply route for %s: %w", partID, err)
			}
		}
		if notifier.partReceipts != nil {
			if err := notifier.partReceipts.ConfirmPart(context.WithoutCancel(ctx), operationID, part); err != nil {
				receipt.State = DeliveryUnknown
				_ = notifier.partReceipts.MarkPartUnknown(context.WithoutCancel(ctx), operationID, partID)
				return receipt, fmt.Errorf("persist Telegram part receipt %s: %w", partID, err)
			}
		}
		receipt.Parts = append(receipt.Parts, part)
	}
	return receipt, nil
}

func notificationPages(notification telegramcontroller.Notification) ([]telegramui.ContentPage, string, error) {
	if notification.ConversationID <= 0 {
		return nil, "", errors.New("Telegram notification conversation id must be positive")
	}
	shortID, err := logicalSessionShortID(notification.SessionID)
	if err != nil {
		return nil, "", err
	}
	kind, err := notificationKind(notification.Kind)
	if err != nil {
		return nil, "", err
	}
	if !utf8.ValidString(notification.Text) || strings.TrimSpace(notification.Text) == "" {
		return nil, "", errors.New("Telegram notification text must be non-empty valid UTF-8")
	}
	prefix := "Сессия " + shortID + " - " + kind + "\n"
	pagination, err := telegramui.PaginateContent([]telegramui.ContentBlock{{
		Anchor: "notification", Content: notification.Text,
	}}, telegramui.PageLimits{
		MaxRunes: telegramTextLimit - utf8.RuneCountInString(prefix),
		MaxBytes: telegramTextLimit - len(prefix),
	})
	if err != nil {
		return nil, "", errors.New("Telegram notification could not be paginated")
	}
	return pagination.Pages, prefix, nil
}

func deliveryPartID(operationID string, part, total int) string {
	return fmt.Sprintf("%s:part:%d-of-%d", operationID, part, total)
}

func deliveryFailureState(err error) DeliveryState {
	var apiError *telegram.APIError
	if errors.As(err, &apiError) && apiError.HTTPStatus >= 400 && apiError.HTTPStatus < 500 {
		return DeliveryFailed
	}
	return DeliveryUnknown
}
