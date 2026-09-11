package telegramstate

import (
	"errors"

	"bria/internal/domain"
)

// CommitNativeCarrier preserves the semantic card while rebinding its exact
// physical Telegram carrier and immutable owner operation.
func CommitNativeCarrier(state *State, id domain.SessionID, carrier Carrier, operation string) error {
	if carrier.ChatID <= 0 || carrier.MessageID <= 0 {
		return errors.New("native carrier receipt must be confirmed")
	}
	card, ok := state.Card(id)
	if !ok {
		return errors.New("native carrier session no longer exists")
	}
	if card.Carrier != carrier {
		card.CarrierOperation, card.Carrier = operation, carrier
	}
	return state.SetCard(card)
}
