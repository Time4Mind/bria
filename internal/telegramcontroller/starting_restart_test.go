package telegramcontroller_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

func TestRestartPreservesActiveStartingSessionAndEarlyFIFO(t *testing.T) {
	starting, err := domain.NewStartingSession(
		"99999999-9999-4999-9999-999999999999",
		"standby:restart",
		"local",
		domain.ProviderCodex,
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newLockedSessions(starting)
	ui := &nodeUIStateStub{
		selected: "local",
		active: map[domain.ComputerID]domain.SessionID{
			"local": starting.ID(),
		},
	}
	accepted := make(chan telegramcontroller.SessionInput, 1)
	custody := durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
		accepted <- input
		return telegramcontroller.InputReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
	})
	controller := newController(t, nil, store, nil, nil, telegramcontroller.Options{
		Recovered:    []domain.Session{starting},
		UIState:      ui,
		DurableInput: custody,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	if _, err := controller.Handle(context.Background(), message(9030, "queued while restarting")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-accepted:
		if input.SessionID != starting.ID() || input.MessageID != "telegram-update:9030" {
			t.Fatalf("accepted input=%#v", input)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("persisted Starting session lost active FIFO after restart")
	}

	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-restarted", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	store.Set(ready)
	controller.RefreshRecoveryCard(context.Background(), ready.ID())
	projected, err := controller.ProjectCurrent(context.Background(), ready.ID())
	if err != nil || projected.Card == nil || projected.Card.SessionID != ready.ID() {
		t.Fatalf("ready projection=(%#v, %v)", projected, err)
	}
}

func TestRestartKeepsBackgroundStartingSessionSelectableForFIFO(t *testing.T) {
	active := readySession(t, "88888888-8888-4888-9888-888888888888", domain.ProviderCodex, t.TempDir(), "provider-active", 1)
	starting, err := domain.NewStartingSession(
		"77777777-7777-4777-9777-777777777777", "standby:background-restart", "local", domain.ProviderCodex, t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newLockedSessions(active, starting)
	ui := &nodeUIStateStub{selected: "local", active: map[domain.ComputerID]domain.SessionID{"local": active.ID()}}
	accepted := make(chan telegramcontroller.SessionInput, 1)
	controller := newController(t, nil, store, nil, nil, telegramcontroller.Options{
		Recovered: []domain.Session{active, starting}, UIState: ui,
		DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			accepted <- input
			return telegramcontroller.InputReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	selected, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: starting.ID()})
	if err != nil || selected.Card == nil || selected.Card.SessionID != starting.ID() {
		t.Fatalf("select restored Starting=(%#v, %v)", selected, err)
	}
	if _, err := controller.Handle(context.Background(), message(9031, "queued after selecting restored standby")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-accepted:
		if input.SessionID != starting.ID() {
			t.Fatalf("FIFO target=%s", input.SessionID)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("restored background Starting session rejected FIFO input")
	}
}

func TestProductionOrderSynchronizesBackgroundStartingAfterControllerConstruction(t *testing.T) {
	active := readySession(t, "66666666-6666-4666-9666-666666666666", domain.ProviderCodex, t.TempDir(), "provider-active", 1)
	starting, err := domain.NewStartingSession(
		"55555555-5555-4555-9555-555555555555", "standby:production-restart", "local", domain.ProviderCodex, t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newLockedSessions(active, starting)
	ui := &nodeUIStateStub{selected: "local", active: map[domain.ComputerID]domain.SessionID{"local": active.ID()}}
	accepted := make(chan telegramcontroller.SessionInput, 1)
	controller := newController(t, nil, store, nil, nil, telegramcontroller.Options{
		// Production binds the controller before Manager.RecoverStartup, so this
		// list is initially empty and committed recovery states arrive via refresh.
		UIState: ui,
		DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			accepted <- input
			return telegramcontroller.InputReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	controller.RefreshRecoveryCard(context.Background(), active.ID())
	controller.RefreshRecoveryCard(context.Background(), starting.ID())

	selected, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: starting.ID()})
	if err != nil || selected.Card == nil || selected.Card.SessionID != starting.ID() {
		t.Fatalf("select synchronized Starting=(%#v, %v)", selected, err)
	}
	if _, err := controller.Handle(context.Background(), message(9032, "queued after production-order recovery")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-accepted:
		if input.SessionID != starting.ID() || input.MessageID != "telegram-update:9032" {
			t.Fatalf("FIFO target=%#v", input)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("production-order recovery did not restore background Starting FIFO")
	}
}
