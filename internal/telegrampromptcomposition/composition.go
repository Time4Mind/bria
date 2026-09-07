// Package telegrampromptcomposition refreshes the active Telegram card when a
// durable user prompt changes delivery state.
package telegrampromptcomposition

import (
	"context"
	"errors"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type Controller interface {
	ProjectCurrent(context.Context, domain.SessionID) (telegramcontroller.SemanticActionResult, error)
}

type CardStore interface {
	Load(context.Context) (telegramstate.State, error)
}

type Sender interface {
	Register(telegramflow.Prepared) error
	EditStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error)
}

type Deliverer struct {
	Controller Controller
	Cards      CardStore
	Presenter  *telegrambridge.Presenter
	Sender     Sender
}

func (deliverer Deliverer) Deliver(ctx context.Context, notification telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
	receipt := telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryUnknown}
	if deliverer.Controller == nil || deliverer.Cards == nil || deliverer.Presenter == nil || deliverer.Sender == nil ||
		(notification.Kind != telegramcontroller.NotificationPromptStatus && notification.Kind != telegramcontroller.NotificationNativeScreen) || notification.SessionID == "" || operationID == "" {
		return receipt, errors.New("prompt status delivery identity is invalid")
	}
	if notification.Kind == telegramcontroller.NotificationNativeScreen {
		if view, ok := deliverer.Controller.(interface {
			NativeDeliveryContext(context.Context, domain.SessionID) (context.Context, context.CancelFunc, bool)
		}); ok {
			guarded, cancel, visible := view.NativeDeliveryContext(ctx, notification.SessionID)
			defer cancel()
			if !visible {
				receipt.State, receipt.Suppressed = telegramnotify.DeliveryConfirmed, true
				return receipt, nil
			}
			ctx = guarded
		}
	}
	if visibility, ok := deliverer.Controller.(interface{ NativeScreenVisible(domain.SessionID) bool }); ok && !visibility.NativeScreenVisible(notification.SessionID) {
		receipt.State, receipt.Suppressed = telegramnotify.DeliveryConfirmed, true
		return receipt, nil
	}
	state, err := deliverer.Cards.Load(ctx)
	if err != nil {
		return receipt, err
	}
	stored, ok := state.Card(notification.SessionID)
	if !ok || stored.Carrier.ChatID <= 0 || stored.Carrier.MessageID <= 0 {
		return receipt, errors.New("active prompt card carrier is not confirmed")
	}
	if state.ActiveSession != notification.SessionID {
		receipt.State = telegramnotify.DeliveryConfirmed
		receipt.Parts = []telegramnotify.PartReceipt{{PartID: operationID + ":stored", MessageID: stored.Carrier.MessageID}}
		return receipt, nil
	}
	result, err := deliverer.Controller.ProjectCurrent(ctx, notification.SessionID)
	if err == nil && result.Surface != nil && result.Surface.NativeSessionID == notification.SessionID {
		if notification.Kind == telegramcontroller.NotificationNativeScreen {
			return deliverer.deliverNativeSurface(ctx, operationID, notification.SessionID, stored.Carrier, *result.Surface)
		}
		// Prompt state is already durable. Do not replace an explicitly shown
		// CLI screen or invalidate its keys with an unrelated history refresh.
		receipt.State = telegramnotify.DeliveryConfirmed
		receipt.Suppressed = true
		receipt.Parts = []telegramnotify.PartReceipt{{PartID: operationID + ":stored", MessageID: stored.Carrier.MessageID}}
		return receipt, nil
	}
	if err != nil || result.Card == nil {
		return receipt, errors.Join(err, errors.New("project active prompt card"))
	}
	card := result.Card
	pages := make([]telegramui.ContentPage, len(card.Pages))
	for index, page := range card.Pages {
		pages[index] = telegramui.ContentPage{Content: page.Content, Anchors: append([]string(nil), page.Anchors...)}
	}
	view := telegramui.PageView{Page: card.View.Page, Pages: card.View.Pages, Anchor: card.View.Anchor, FollowLatest: card.View.FollowLatest}
	prepared, err := telegramflow.PrepareCardRefresh(operationID, card.SessionID, stored.Carrier.ChatID, stored.Carrier.MessageID,
		telegramui.CardProjectionInput{Pages: pages, View: view, Keyboard: telegramui.CardKeyboardInput{
			View: view, Working: card.Working, Archived: card.Archived, OptionsExpanded: card.OptionsExpanded,
			SessionRowSizes: append([]int(nil), card.SessionRowSizes...),
			SessionLabels:   append([]string(nil), card.SelectableSessionLabels...),
		}}, card.Header+"\n\n", card.OptionsExpanded, card.SelectableSessionIDs, deliverer.Presenter)
	if err != nil {
		return receipt, err
	}
	if err := deliverer.Sender.Register(prepared); err != nil {
		return receipt, err
	}
	telegramReceipt, err := deliverer.Sender.EditStatusWithKeyboard(ctx, operationID, prepared.Status, prepared.Keyboard)
	if err != nil || telegramReceipt.MessageID <= 0 {
		return receipt, errors.Join(err, errors.New("Telegram prompt card refresh is unconfirmed"))
	}
	receipt.State = telegramnotify.DeliveryConfirmed
	receipt.Parts = []telegramnotify.PartReceipt{{PartID: operationID + ":1/1", MessageID: telegramReceipt.MessageID}}
	return receipt, nil
}
