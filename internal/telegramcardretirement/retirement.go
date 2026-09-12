// Package telegramcardretirement owns exact-carrier keyboard retirement before
// a replacement session card is sent.
package telegramcardretirement

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

var ErrStale = errors.New("stale Telegram card retirement")

type Plan struct {
	SessionID       domain.SessionID      `json:"session_id"`
	Carrier         telegramstate.Carrier `json:"carrier"`
	CarrierRevision uint64                `json:"carrier_revision"`
}

type Store interface {
	Load(context.Context) (telegramstate.State, error)
}

type Deactivator interface {
	DeactivateInlineKeyboard(context.Context, string, int64, int64) error
}

type Invalidator interface {
	InvalidateCarrier(context.Context, telegramstate.Carrier) error
}

func Capture(ctx context.Context, store Store, sessionID domain.SessionID, eligible bool) (*Plan, error) {
	if store == nil || sessionID == "" || !eligible {
		return nil, nil
	}
	state, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	card, exists := state.Card(sessionID)
	if !exists || card.Carrier.ChatID <= 0 || card.Carrier.MessageID <= 0 {
		return nil, nil
	}
	if foreignActiveOwnsCarrier(state, sessionID, card.Carrier) {
		return nil, nil
	}
	return &Plan{SessionID: sessionID, Carrier: card.Carrier, CarrierRevision: card.CarrierRevision}, nil
}

func Execute(ctx context.Context, store Store, deactivator Deactivator, invalidator Invalidator, operationID string, conversationID int64, sessionID domain.SessionID, eligible bool, plan *Plan) error {
	if plan == nil || !eligible {
		return nil
	}
	if store == nil || deactivator == nil || invalidator == nil || plan.SessionID == "" || plan.SessionID != sessionID ||
		plan.Carrier.ChatID != conversationID || plan.Carrier.MessageID <= 0 {
		return errors.New("previous Telegram card retirement identity is invalid")
	}
	state, err := store.Load(ctx)
	if err != nil {
		return err
	}
	current, exists := state.Card(plan.SessionID)
	if !exists || current.Carrier != plan.Carrier || current.CarrierRevision != plan.CarrierRevision {
		return ErrStale
	}
	if foreignActiveOwnsCarrier(state, plan.SessionID, plan.Carrier) {
		return nil
	}
	if err := deactivator.DeactivateInlineKeyboard(ctx, operationID+":retire_previous", plan.Carrier.ChatID, plan.Carrier.MessageID); err != nil {
		return fmt.Errorf("retire previous Telegram card: %w", err)
	}
	if err := invalidator.InvalidateCarrier(ctx, plan.Carrier); err != nil {
		return fmt.Errorf("invalidate previous Telegram card: %w", err)
	}
	return nil
}

func foreignActiveOwnsCarrier(state telegramstate.State, sessionID domain.SessionID, carrier telegramstate.Carrier) bool {
	if state.ActiveSession == "" || state.ActiveSession == sessionID {
		return false
	}
	active, exists := state.Card(state.ActiveSession)
	return exists && active.Carrier == carrier
}
