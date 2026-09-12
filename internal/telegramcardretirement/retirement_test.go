package telegramcardretirement_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcardretirement"
	"bria/internal/telegramstate"
)

type deactivatorStub struct{ calls int }

func (stub *deactivatorStub) DeactivateInlineKeyboard(context.Context, string, int64, int64) error {
	stub.calls++
	return nil
}

type invalidatorStub struct{ calls int }

func (stub *invalidatorStub) InvalidateCarrier(context.Context, telegramstate.Carrier) error {
	stub.calls++
	return nil
}

func TestCaptureAndExecuteRetireOnlyExactCarrier(t *testing.T) {
	ctx := context.Background()
	const sessionID domain.SessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		return state.SetCard(telegramstate.Card{SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 7}, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := telegramcardretirement.Capture(ctx, store, sessionID, true)
	if err != nil || plan == nil || plan.Carrier.MessageID != 7 || plan.CarrierRevision != 1 {
		t.Fatalf("capture = %#v, %v", plan, err)
	}
	deactivator, invalidator := &deactivatorStub{}, &invalidatorStub{}
	if err := telegramcardretirement.Execute(ctx, store, deactivator, invalidator, "status:1", 42, sessionID, true, plan); err != nil {
		t.Fatal(err)
	}
	if deactivator.calls != 1 || invalidator.calls != 1 {
		t.Fatalf("retirement calls = %d/%d", deactivator.calls, invalidator.calls)
	}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		card, _ := state.Card(sessionID)
		card.Carrier = telegramstate.Carrier{ChatID: 42, MessageID: 8}
		return state.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	if err := telegramcardretirement.Execute(ctx, store, deactivator, invalidator, "status:1", 42, sessionID, true, plan); !errors.Is(err, telegramcardretirement.ErrStale) {
		t.Fatalf("stale execution error = %v", err)
	}
	if deactivator.calls != 1 || invalidator.calls != 1 {
		t.Fatal("stale plan touched replacement carrier")
	}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		card, _ := state.Card(sessionID)
		card.Carrier = telegramstate.Carrier{ChatID: 42, MessageID: 7}
		return state.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	if err := telegramcardretirement.Execute(ctx, store, deactivator, invalidator, "status:1", 42, sessionID, true, plan); !errors.Is(err, telegramcardretirement.ErrStale) {
		t.Fatalf("ABA execution error = %v", err)
	}
	if deactivator.calls != 1 || invalidator.calls != 1 {
		t.Fatal("ABA plan touched reused replacement carrier")
	}
}

func TestCaptureDoesNotRetireCarrierOwnedByAnotherActiveSession(t *testing.T) {
	ctx := context.Background()
	const (
		activeID     domain.SessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		backgroundID domain.SessionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	shared := telegramstate.Carrier{ChatID: 42, MessageID: 7}
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = activeID
		if err := state.SetCard(telegramstate.Card{SessionID: activeID, Carrier: shared, Page: telegramstate.Page{Current: 1, Total: 1}}); err != nil {
			return err
		}
		return state.SetCard(telegramstate.Card{SessionID: backgroundID, Carrier: shared, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := telegramcardretirement.Capture(ctx, store, backgroundID, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("foreign active carrier was captured for retirement: %#v", plan)
	}
}

func TestDelayedRetirementDoesNotTouchCarrierAdoptedByAnotherActiveSession(t *testing.T) {
	ctx := context.Background()
	const (
		activeID     domain.SessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		backgroundID domain.SessionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	shared := telegramstate.Carrier{ChatID: 42, MessageID: 7}
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = backgroundID
		return state.SetCard(telegramstate.Card{SessionID: backgroundID, Carrier: shared, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := telegramcardretirement.Capture(ctx, store, backgroundID, true)
	if err != nil || plan == nil {
		t.Fatalf("capture = %#v, %v", plan, err)
	}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = activeID
		return state.SetCard(telegramstate.Card{SessionID: activeID, Carrier: shared, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	deactivator, invalidator := &deactivatorStub{}, &invalidatorStub{}
	if err := telegramcardretirement.Execute(ctx, store, deactivator, invalidator, "status:2", 42, backgroundID, true, plan); err != nil {
		t.Fatalf("foreign active carrier must be skipped without blocking notification: %v", err)
	}
	if deactivator.calls != 0 || invalidator.calls != 0 {
		t.Fatalf("foreign active carrier was touched: %d/%d", deactivator.calls, invalidator.calls)
	}
}
