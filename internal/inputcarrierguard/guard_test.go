package inputcarrierguard_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/inputcarrierguard"
	"bria/internal/telegramstate"
)

func TestAwaitAdmitsConsecutiveStatusesForSameInputCarrier(t *testing.T) {
	const sessionID = domain.SessionID("session")
	store := telegramstate.NewMemoryStore()
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = sessionID
		return state.SetCard(telegramstate.Card{
			SessionID: sessionID,
			Carrier:   telegramstate.Carrier{ChatID: 42, MessageID: 77},
			Page:      telegramstate.Page{Current: 1, Total: 1},
			// The first status refresh becomes the latest presentation, but the
			// carrier still belongs to Telegram input 71.
			LastPresentationOperation: "telegram-update:71:prompt-status:🙋‍♂",
		})
	}); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.Load(context.Background())
	card, _ := stored.Card(sessionID)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := inputcarrierguard.Await(ctx, store, sessionID, "telegram-update:71:prompt-status:preprocessed", card)
	if err != nil || !got.Ready || got.Outcome != inputcarrierguard.OutcomeMatched || got.Card.Carrier.MessageID != 77 {
		t.Fatalf("same-input status = (%+v, %v), want immediate current carrier", got, err)
	}
}

func TestAwaitStillWaitsForDifferentInputCarrier(t *testing.T) {
	const sessionID = domain.SessionID("session")
	store := telegramstate.NewMemoryStore()
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = sessionID
		return state.SetCard(telegramstate.Card{
			SessionID:                 sessionID,
			Carrier:                   telegramstate.Carrier{ChatID: 42, MessageID: 77},
			Page:                      telegramstate.Page{Current: 1, Total: 1},
			LastPresentationOperation: "telegram-update:70:prompt-status:preprocessed",
		})
	}); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.Load(context.Background())
	card, _ := stored.Card(sessionID)
	type outcome struct {
		result inputcarrierguard.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := inputcarrierguard.Await(context.Background(), store, sessionID, "telegram-update:71:prompt-status:preprocessed", card)
		done <- outcome{result: got, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("different input reused previous carrier: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		current, _ := state.Card(sessionID)
		current.Carrier.MessageID = 78
		current.LastPresentationOperation = "status:71"
		return state.SetCard(current)
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.result.Ready || got.result.Outcome != inputcarrierguard.OutcomeMatched || got.result.Card.Carrier.MessageID != 78 {
			t.Fatalf("new input carrier = %+v, want ready carrier 78", got)
		}
	case <-time.After(time.Second):
		t.Fatal("different input did not resume on its own carrier")
	}
}

func TestAwaitSuppressesDifferentInputWhenAnotherCarrierWins(t *testing.T) {
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
	done := make(chan inputcarrierguard.Result, 1)
	go func() {
		result, _ := inputcarrierguard.Await(context.Background(), store, sessionID, "telegram-update:71:prompt-status:preprocessed", card)
		done <- result
	}()
	time.Sleep(30 * time.Millisecond)
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		current, _ := state.Card(sessionID)
		current.Carrier.MessageID = 72
		current.LastPresentationOperation = "status:72"
		return state.SetCard(current)
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Ready || result.Outcome != inputcarrierguard.OutcomeSuperseded || result.Card.Carrier.MessageID != 72 {
			t.Fatalf("superseded result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("superseded input did not stop waiting")
	}
}
