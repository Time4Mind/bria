package telegramnotify

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"bria/internal/notificationplan"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

type pagePlanStore interface {
	PartReceiptStore
	UnknownPartStore
	LoadPlan(context.Context, string, string) (notificationplan.Plan, bool, error)
	GetOrCreatePlan(context.Context, string, string, []string, []string) (notificationplan.Plan, error)
	ClaimPart(context.Context, string, string) (bool, error)
	ReleasePart(context.Context, string, string) error
}

func (notifier *Notifier) deliveryPages(ctx context.Context, notification telegramcontroller.Notification, operationID string) ([]string, pagePlanStore, error) {
	prefix, err := notificationPrefix(notification)
	if err != nil {
		return nil, nil, err
	}
	// The hash binds source identity, not a recomputed rendering. A saved plan
	// remains authoritative across renderer changes and process restarts.
	input, err := json.Marshal([]any{notification.ConversationID, notification.SessionID, notification.Kind, notification.Text})
	if err != nil {
		return nil, nil, errors.New("invalid notification identity")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(input))
	plans, durable := notifier.partReceipts.(pagePlanStore)
	if durable {
		plan, found, err := plans.LoadPlan(ctx, operationID, hash)
		if err != nil || found {
			return plan.Pages, plans, err
		}
	}
	legacy, err := legacyNotificationPages(notification)
	if err != nil {
		return nil, plans, err
	}
	if notifier.partReceipts != nil && !durable {
		// Third-party receipt-only stores retain their pre-plan compatibility.
		return legacy, nil, nil
	}
	pages, err := notificationplan.Rich(prefix, notification.Text, telegramTextLimit)
	if err != nil {
		return nil, plans, err
	}
	if durable {
		plan, err := plans.GetOrCreatePlan(ctx, operationID, hash, pages, legacy)
		return plan.Pages, plans, err
	}
	return pages, nil, nil
}

func legacyNotificationPages(notification telegramcontroller.Notification) ([]string, error) {
	pages, prefix, err := notificationPages(notification)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(pages))
	for index, page := range pages {
		result[index] = telegram.NormalizeRichMarkdown(prefix + page.Content)
	}
	return result, nil
}

func claimNotificationPage(ctx context.Context, plans pagePlanStore, operationID, partID string) (int64, error) {
	claimed, err := plans.ClaimPart(ctx, operationID, partID)
	if err != nil || claimed {
		return 0, err
	}
	confirmed, err := plans.ConfirmedParts(ctx, operationID)
	if err != nil {
		return 0, err
	}
	for _, part := range confirmed {
		if part.PartID == partID {
			return part.MessageID, nil
		}
	}
	return 0, errors.New("Telegram notification part is already claimed or unknown")
}
