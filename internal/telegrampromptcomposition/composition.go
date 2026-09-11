package telegrampromptcomposition

import (
	"context"
	"errors"
	"time"

	"bria/internal/carddeliveryguard"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/inputcarrierguard"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegramui"
)

type Controller interface {
	ProjectCurrent(context.Context, domain.SessionID) (telegramcontroller.SemanticActionResult, error)
}

type CardStore = inputcarrierguard.Store

type Sender interface {
	Register(telegramflow.Prepared) error
	EditStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error)
}

type Deliverer struct {
	Controller Controller
	Cards      CardStore
	Presenter  *telegrambridge.Presenter
	Sender     Sender
	Observer   telegramflow.TraceObserver
}

func (deliverer Deliverer) Deliver(ctx context.Context, notification telegramcontroller.Notification, operationID string) (receipt telegramnotify.DeliveryReceipt, err error) {
	receipt = telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryUnknown}
	if deliverer.Controller == nil || deliverer.Cards == nil || deliverer.Presenter == nil || deliverer.Sender == nil ||
		(notification.Kind != telegramcontroller.NotificationPromptStatus && notification.Kind != telegramcontroller.NotificationNativeScreen) || notification.SessionID == "" || operationID == "" {
		return receipt, errors.New("prompt status delivery identity is invalid")
	}
	scope := carddeliveryguard.Capture(ctx, deliverer.Controller, notification.SessionID)
	defer func() {
		if scope.Suppressed(err) {
			receipt = telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryConfirmed, Suppressed: true}
			err = nil
		}
		scope.Close()
	}()
	if err = scope.Context.Err(); err != nil {
		return receipt, err
	}
	if !scope.Visible {
		deliverer.observeGuard(ctx, notification.SessionID, operationID, inputcarrierguard.Result{}, time.Now(), nil, true)
		receipt.State, receipt.Suppressed = telegramnotify.DeliveryConfirmed, true
		return receipt, nil
	}
	ctx = scope.Context
	state, err := deliverer.Cards.Load(ctx)
	if err != nil {
		return receipt, err
	}
	stored, ok := state.Card(notification.SessionID)
	if !ok || stored.Carrier.ChatID <= 0 || stored.Carrier.MessageID <= 0 {
		return receipt, errors.New("active prompt card carrier is not confirmed")
	}
	guardStarted := time.Now()
	guard, err := inputcarrierguard.Await(ctx, deliverer.Cards, notification.SessionID, operationID, stored)
	deliverer.observeGuard(ctx, notification.SessionID, operationID, guard, guardStarted, err, scope.Suppressed(err))
	if err != nil {
		return receipt, err
	}
	stored = guard.Card
	if !guard.Ready {
		receipt.State, receipt.Suppressed = telegramnotify.DeliveryConfirmed, true
		return receipt, nil
	}
	if err = carddeliveryguard.Check(ctx, deliverer.Cards, notification.SessionID, stored.Carrier); err != nil {
		return receipt, err
	}
	result, err := deliverer.Controller.ProjectCurrent(ctx, notification.SessionID)
	if err == nil {
		err = carddeliveryguard.Check(ctx, deliverer.Cards, notification.SessionID, stored.Carrier)
	}
	if err == nil && result.Surface != nil && result.Surface.NativeSessionID == notification.SessionID {
		if notification.Kind == telegramcontroller.NotificationNativeScreen {
			return deliverer.deliverNativeSurface(ctx, operationID, notification.SessionID, stored.Carrier, stored.CarrierRevision, *result.Surface)
		}
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
		pages[index] = telegramui.ContentPage{Content: page.Content, Anchors: append([]string(nil), page.Anchors...), FinalStart: page.FinalStart, FinalOperationID: page.FinalOperationID}
	}
	view := telegramui.PageView{Page: card.View.Page, Pages: card.View.Pages, Anchor: card.View.Anchor, FollowLatest: card.View.FollowLatest}
	prepared, err := telegramflow.PrepareCardRefresh(operationID, card.SessionID, stored.Carrier.ChatID, stored.Carrier.MessageID,
		telegramui.CardProjectionInput{Pages: pages, View: view, Keyboard: telegramui.CardKeyboardInput{
			View: view, Working: card.Working, Archived: card.Archived, Recovery: card.Recovery, OptionsExpanded: card.OptionsExpanded,
			SessionRowSizes: append([]int(nil), card.SessionRowSizes...),
			SessionLabels:   append([]string(nil), card.SelectableSessionLabels...),
		}}, card.Header, card.OptionsExpanded, card.SelectableSessionIDs, deliverer.Presenter)
	if err != nil {
		return receipt, err
	}
	revision := stored.CarrierRevision
	prepared.Card.ExpectedCarrierRevision = &revision
	prepared.Card.ExpectedPresentationOperation = &stored.LastPresentationOperation
	if err := deliverer.Sender.Register(prepared); err != nil {
		return receipt, err
	}
	if err = carddeliveryguard.Check(ctx, deliverer.Cards, notification.SessionID, stored.Carrier); err != nil {
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
func (deliverer Deliverer) observeGuard(ctx context.Context, sessionID domain.SessionID, operationID string, result inputcarrierguard.Result, started time.Time, err error, navigation bool) {
	if deliverer.Observer == nil {
		return
	}
	outcome, reason := string(result.Outcome), ""
	if navigation {
		outcome, reason = "navigation_suppressed", "cancelled"
	} else if err != nil {
		outcome, reason = "failed", "operation_failed"
		if errors.Is(err, context.Canceled) {
			reason = "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			reason = "deadline_exceeded"
		}
	}
	deliverer.Observer.ObserveTelegramFlow(context.WithoutCancel(ctx), telegramflow.TraceEvent{
		Stage: "prompt.carrier_guard", OperationID: operationID, SessionID: string(sessionID),
		ChatID: result.Card.Carrier.ChatID, CarrierID: result.Card.Carrier.MessageID,
		Result: outcome, Reason: reason, Duration: time.Since(started),
	})
}
