package inputcarrierguard

import (
	"context"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

func TestAwaitClassifiesTimeoutWithoutReusingPreviousCarrier(t *testing.T) {
	const sessionID = domain.SessionID("session")
	store := telegramstate.NewMemoryStore()
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = sessionID
		return state.SetCard(telegramstate.Card{
			SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 70},
			Page: telegramstate.Page{Current: 1, Total: 1}, LastPresentationOperation: "status:70",
		})
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load(context.Background())
	card, _ := state.Card(sessionID)
	result, err := await(context.Background(), store, sessionID, "telegram-update:71:prompt-status:preprocessed", card, 5*time.Millisecond, time.Millisecond)
	if err != nil || result.Ready || result.Outcome != OutcomeTimeout || result.Card.Carrier.MessageID != 70 {
		t.Fatalf("timeout result = (%+v, %v)", result, err)
	}
}
