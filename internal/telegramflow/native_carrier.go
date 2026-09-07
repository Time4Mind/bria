package telegramflow

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func validateNativeSurface(surface SurfaceOutput) error {
	id := surface.NativeSessionID
	if id == "" {
		return nil
	}
	if !callbacktoken.IsCanonicalSessionID(string(id)) || string(id) == telegramui.GlobalSurfaceID {
		return errors.New("native carrier requires exact logical session ID")
	}
	if surface.InteractionSessionID != "" || surface.InteractionRequestID != "" || surface.OutboundOperationID != "" || surface.OutboundUpdateID != 0 || surface.Recovery != nil || surface.AcceptedTurnRecovery != nil || surface.StatusRecovery != nil || surface.ArtifactRetry != nil {
		return errors.New("native carrier cannot include another special surface binding")
	}
	found := false
	for _, row := range surface.Keyboard.Rows {
		for _, button := range row {
			if !telegramui.IsSessionSurfaceAction(button.Action) {
				continue
			}
			slot := button.Target.SessionSlot
			if slot < 1 || slot > len(surface.SelectableSessionIDs) || surface.SelectableSessionIDs[slot-1] != id {
				return errors.New("native carrier and native control session differ")
			}
			found = true
		}
	}
	if !found {
		return errors.New("native carrier requires exact native controls")
	}
	return nil
}

func commitNativeCarrier(ctx context.Context, store telegramstate.Store, id domain.SessionID, carrier telegramstate.Carrier) error {
	if carrier.ChatID <= 0 || carrier.MessageID <= 0 {
		return errors.New("native carrier receipt must be confirmed")
	}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		card, ok := state.Card(id)
		if !ok {
			return errors.New("native carrier session no longer exists")
		}
		// Do not reconstruct a card from terminal screen text: prompts, history
		// keys, pagination, empty-session evidence and active selection survive.
		card.Carrier = carrier
		return state.SetCard(card)
	}); err != nil {
		return fmt.Errorf("commit confirmed native carrier: %w", err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		return err
	}
	card, ok := state.Card(id)
	if !ok || card.Carrier != carrier {
		return errors.New("confirmed native carrier reread mismatch")
	}
	return nil
}
