package telegramnotify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"bria/internal/notificationstate"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramui"
)

type DeliveryState = notificationstate.DeliveryState

const (
	DeliveryConfirmed = notificationstate.DeliveryConfirmed
	DeliveryFailed    = notificationstate.DeliveryFailed
	DeliveryUnknown   = notificationstate.DeliveryUnknown
)

type PartReceipt = notificationstate.PartReceipt

type DeliveryReceipt struct {
	OperationID string
	State       DeliveryState
	Parts       []PartReceipt
	// Suppressed confirms that policy intentionally performed no Telegram
	// mutation, for example a background intermediate card projection.
	Suppressed bool
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
	pages, plans, err := notifier.deliveryPages(ctx, notification, operationID)
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
		if plans != nil {
			messageID, claimErr := claimNotificationPage(ctx, plans, operationID, partID)
			if claimErr != nil {
				receipt.State = DeliveryUnknown
				return receipt, claimErr
			}
			if messageID > 0 {
				receipt.Parts = append(receipt.Parts, PartReceipt{PartID: partID, MessageID: messageID})
				continue
			}
		}
		message, sendErr := notifier.client.SendRichMessage(ctx, telegram.SendRichMessageRequest{
			ChatID: telegram.ChatID(notification.ConversationID),
			RichMessage: telegram.InputRichMessage{
				Markdown: page,
			},
		})
		if sendErr != nil || message.MessageID <= 0 {
			receipt.State = deliveryFailureState(sendErr)
			if plans != nil && receipt.State == DeliveryFailed {
				if releaseErr := plans.ReleasePart(context.WithoutCancel(ctx), operationID, partID); releaseErr != nil {
					receipt.State = DeliveryUnknown
					sendErr = errors.Join(sendErr, releaseErr)
				}
			}
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
	prefix, err := notificationPrefix(notification)
	if err != nil {
		return nil, "", err
	}
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

func notificationPrefix(notification telegramcontroller.Notification) (string, error) {
	if notification.ConversationID <= 0 {
		return "", errors.New("Telegram notification conversation id must be positive")
	}
	shortID, err := logicalSessionShortID(notification.SessionID)
	if err != nil {
		return "", err
	}
	kind, err := notificationKind(notification.Kind)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(notification.Text) || strings.TrimSpace(notification.Text) == "" {
		return "", errors.New("Telegram notification text must be non-empty valid UTF-8")
	}
	return "Сессия " + shortID + " - " + kind + "\n", nil
}

func deliveryPartID(operationID string, part, total int) string {
	return fmt.Sprintf("%s:part:%d-of-%d", operationID, part, total)
}

func deliveryFailureState(err error) DeliveryState {
	if errors.Is(err, telegram.ErrDeliveryUnknown) {
		return DeliveryUnknown
	}
	var apiError *telegram.APIError
	if errors.As(err, &apiError) && apiError.HTTPStatus >= 400 && apiError.HTTPStatus < 500 {
		return DeliveryFailed
	}
	return DeliveryUnknown
}
