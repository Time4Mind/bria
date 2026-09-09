package telegrampromptcomposition

import (
	"context"
	"errors"

	"bria/internal/carddeliveryguard"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func (d Deliverer) deliverNativeSurface(ctx context.Context, operation string, id domain.SessionID, carrier telegramstate.Carrier, surface telegramcontroller.SemanticSurface) (telegramnotify.DeliveryReceipt, error) {
	receipt := telegramnotify.DeliveryReceipt{OperationID: operation, State: telegramnotify.DeliveryUnknown}
	keyboard := telegramui.CardKeyboard{}
	var selectable []domain.SessionID
	for _, row := range surface.Rows {
		buttons := telegramui.ButtonRow{}
		for _, b := range row {
			button := telegramui.Button{Label: b.Label}
			switch b.Action {
			case telegramcontroller.SemanticNativeKey:
				if b.SessionID != id || b.Choice < 1 || b.Choice > 8 {
					return receipt, errors.New("native key identity is invalid")
				}
				button.Action = telegramui.ActionNativeKey
				button.Target.Choice = b.Choice
			case telegramcontroller.SemanticSelect:
				if b.SessionID != id {
					return receipt, errors.New("native back identity is invalid")
				}
				button.Action = telegramui.ActionSelectSession
			case telegramcontroller.SemanticMenuBack:
				if b.SessionID != "" {
					return receipt, errors.New("native menu identity is invalid")
				}
				button.Action = telegramui.ActionMenuBack
			default:
				return receipt, errors.New("unexpected native surface action")
			}
			if b.SessionID != "" {
				selectable = append(selectable, id)
				button.Target.SessionSlot = len(selectable)
			}
			buttons = append(buttons, button)
		}
		keyboard.Rows = append(keyboard.Rows, buttons)
	}
	prepared, err := telegramflow.PrepareSurface(operation, carrier.ChatID, "", carrier.MessageID, true, telegramflow.SurfaceOutput{Text: surface.Text, Keyboard: keyboard, SelectableSessionIDs: selectable, NativeSessionID: id}, d.Presenter)
	if err != nil {
		return receipt, err
	}
	if err := d.Sender.Register(prepared); err != nil {
		return receipt, err
	}
	if err := carddeliveryguard.Check(ctx, d.Cards, id, carrier); err != nil {
		return receipt, err
	}
	delivered, err := d.Sender.EditStatusWithKeyboard(ctx, operation, prepared.Status, prepared.Keyboard)
	if err != nil || delivered.MessageID <= 0 {
		return receipt, errors.Join(err, errors.New("native surface delivery unconfirmed"))
	}
	receipt.State = telegramnotify.DeliveryConfirmed
	receipt.Parts = []telegramnotify.PartReceipt{{PartID: operation + ":1/1", MessageID: delivered.MessageID}}
	return receipt, nil
}
