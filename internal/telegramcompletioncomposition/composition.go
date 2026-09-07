// Package telegramcompletioncomposition wires durable final output to the
// signed Telegram card flow without adding that transport concern to the
// semantic controller composition.
package telegramcompletioncomposition

import (
	"context"
	"errors"
	"sync"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/settingsport"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type Controller interface {
	ProjectCompletion(context.Context, domain.SessionID) (telegramcontroller.SemanticCard, bool, error)
}

type Deliverer interface {
	Deliver(context.Context, telegramcontroller.Notification, string) (telegramnotify.DeliveryReceipt, error)
}

type Router struct {
	mu       sync.RWMutex
	fallback Deliverer
	finals   Deliverer
	prompts  Deliverer
}

func (router *Router) BindPromptStatuses(prompts Deliverer) error {
	if router == nil || prompts == nil {
		return errors.New("prompt status Telegram deliverer is required")
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.prompts != nil {
		return errors.New("prompt status Telegram deliverer is already bound")
	}
	router.prompts = prompts
	return nil
}

func NewRouter(fallback Deliverer) (*Router, error) {
	if fallback == nil {
		return nil, errors.New("fallback Telegram notifier is required")
	}
	return &Router{fallback: fallback}, nil
}

func (router *Router) BindFinals(finals Deliverer) error {
	if router == nil || finals == nil {
		return errors.New("final Telegram deliverer is required")
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.finals != nil {
		return errors.New("final Telegram deliverer is already bound")
	}
	router.finals = finals
	return nil
}

func (router *Router) Deliver(ctx context.Context, notification telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
	if router == nil || router.fallback == nil {
		return telegramnotify.DeliveryReceipt{}, errors.New("Telegram notification router is required")
	}
	router.mu.RLock()
	deliverer := router.fallback
	if (notification.Kind == telegramcontroller.NotificationFinal ||
		notification.Kind == telegramcontroller.NotificationQuestion ||
		notification.Kind == telegramcontroller.NotificationCommentary ||
		notification.Kind == telegramcontroller.NotificationError) && router.finals != nil {
		deliverer = router.finals
	} else if (notification.Kind == telegramcontroller.NotificationPromptStatus || notification.Kind == telegramcontroller.NotificationNativeScreen) && router.prompts != nil {
		deliverer = router.prompts
	}
	router.mu.RUnlock()
	return deliverer.Deliver(ctx, notification, operationID)
}

type PreparedSender interface {
	Register(telegramflow.Prepared) error
	SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error)
	EditStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error)
}

type CardStore interface {
	Load(context.Context) (telegramstate.State, error)
}

type CompletionDeliverer struct {
	Controller     Controller
	Presenter      *telegrambridge.Presenter
	Sender         PreparedSender
	Cards          CardStore
	Preferences    settingsport.Preferences
	ConversationID int64
}

func (deliverer CompletionDeliverer) Deliver(ctx context.Context, notification telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
	receipt := telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryUnknown}
	if deliverer.Controller == nil || deliverer.Presenter == nil || deliverer.Sender == nil || deliverer.ConversationID <= 0 ||
		!stateNotification(notification.Kind) || notification.SessionID == "" || operationID == "" {
		return receipt, errors.New("completion delivery identity is invalid")
	}
	var storedState telegramstate.State
	var err error
	question := notification.Kind == telegramcontroller.NotificationQuestion
	if question {
		var allowed bool
		storedState, allowed, err = deliverer.questionPolicy(ctx, notification.SessionID)
		if err != nil {
			return receipt, err
		}
		if !allowed {
			receipt.State = telegramnotify.DeliveryConfirmed
			receipt.Suppressed = true
			return receipt, nil
		}
	}
	if notification.Kind == telegramcontroller.NotificationCommentary {
		if deliverer.Cards == nil {
			return receipt, errors.New("commentary card store is required")
		}
		storedState, err = deliverer.Cards.Load(ctx)
		if err != nil {
			return receipt, err
		}
		if storedState.ActiveSession != notification.SessionID {
			receipt.State = telegramnotify.DeliveryConfirmed
			receipt.Suppressed = true
			return receipt, nil
		}
	}
	card, active, err := deliverer.Controller.ProjectCompletion(ctx, notification.SessionID)
	if err != nil {
		return receipt, err
	}
	// A native overlay owns its screen and arrow controls. Its passive observer
	// renders questions; don't replace it with a transcript card or duplicate alert.
	if question && storedState.ActiveSession == notification.SessionID && !active {
		receipt.State = telegramnotify.DeliveryConfirmed
		receipt.Suppressed = true
		return receipt, nil
	}
	if notification.Kind == telegramcontroller.NotificationCommentary && !active {
		receipt.State = telegramnotify.DeliveryConfirmed
		receipt.Suppressed = true
		return receipt, nil
	}
	if notification.Kind == telegramcontroller.NotificationError && !active && deliverer.Preferences != nil {
		preferences, snapshotErr := deliverer.Preferences.Snapshot(ctx)
		if snapshotErr != nil {
			return receipt, snapshotErr
		}
		if !preferences.NotifyBackgroundErrors {
			receipt.State = telegramnotify.DeliveryConfirmed
			receipt.Suppressed = true
			return receipt, nil
		}
	}
	pages := make([]telegramui.ContentPage, len(card.Pages))
	for index, page := range card.Pages {
		pages[index] = telegramui.ContentPage{Content: page.Content, Anchors: append([]string(nil), page.Anchors...)}
	}
	view := telegramui.PageView{Page: card.View.Page, Pages: card.View.Pages, Anchor: card.View.Anchor, FollowLatest: card.View.FollowLatest}
	input := telegramui.CardProjectionInput{
		Pages: pages, View: view,
		Keyboard: telegramui.CardKeyboardInput{
			View: view, Working: card.Working, Archived: card.Archived,
			CloseConfirmation: card.CloseConfirmation, OptionsExpanded: card.OptionsExpanded,
			SessionRowSizes: append([]int(nil), card.SessionRowSizes...),
			SessionLabels:   append([]string(nil), card.SelectableSessionLabels...),
		},
	}
	var prepared telegramflow.Prepared
	if notification.Kind == telegramcontroller.NotificationCommentary || question && active {
		stored, ok := storedState.Card(card.SessionID)
		if !ok || stored.Carrier.ChatID <= 0 || stored.Carrier.MessageID <= 0 {
			return receipt, errors.New("active commentary card carrier is not confirmed")
		}
		prepared, err = telegramflow.PrepareCardRefresh(operationID, card.SessionID, stored.Carrier.ChatID, stored.Carrier.MessageID,
			input, card.Header+"\n\n", card.OptionsExpanded, card.SelectableSessionIDs, deliverer.Presenter)
	} else {
		prepared, err = telegramflow.PrepareCompletion(operationID, card.SessionID, deliverer.ConversationID, active,
			input, card.OptionsExpanded, card.SelectableSessionIDs, deliverer.Presenter)
	}
	if err != nil {
		return receipt, err
	}
	if question && !active {
		prepared.Status.Text = "Фоновая сессия ждёт ответа."
	}
	prepared.Card.Header = card.Header + "\n\n"
	if active {
		prepared.Status.Text = prepared.Card.Header + prepared.Card.Projection.Card.Pages[prepared.Card.Projection.Card.View.Page-1].Content
	}
	if question && active {
		if visibility, ok := deliverer.Controller.(interface{ NativeScreenVisible(domain.SessionID) bool }); ok && !visibility.NativeScreenVisible(notification.SessionID) {
			receipt.State = telegramnotify.DeliveryConfirmed
			receipt.Suppressed = true
			return receipt, nil
		}
	}
	if err := deliverer.Sender.Register(prepared); err != nil {
		return receipt, err
	}
	var telegramReceipt coordinator.Receipt
	if prepared.Edit {
		telegramReceipt, err = deliverer.Sender.EditStatusWithKeyboard(ctx, operationID, prepared.Status, prepared.Keyboard)
	} else {
		telegramReceipt, err = deliverer.Sender.SendStatusWithKeyboard(ctx, operationID, prepared.Status, prepared.Keyboard)
	}
	if err != nil || telegramReceipt.MessageID <= 0 {
		return receipt, errors.Join(err, errors.New("Telegram completion delivery is unconfirmed"))
	}
	receipt.State = telegramnotify.DeliveryConfirmed
	receipt.Parts = []telegramnotify.PartReceipt{{PartID: operationID + ":1/1", MessageID: telegramReceipt.MessageID}}
	return receipt, nil
}

func stateNotification(kind telegramcontroller.NotificationKind) bool {
	return kind == telegramcontroller.NotificationFinal || kind == telegramcontroller.NotificationQuestion || kind == telegramcontroller.NotificationCommentary || kind == telegramcontroller.NotificationError
}
