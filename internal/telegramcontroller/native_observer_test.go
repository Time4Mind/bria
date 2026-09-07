package telegramcontroller_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type observedNative struct {
	nativeFake
	mu       sync.Mutex
	snapshot sessionruntime.NativeSnapshot
	updates  chan domain.SessionID
}

func (n *observedNative) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.snapshot, n.snapshot.Hash != ""
}
func (n *observedNative) NativeScreenUpdates() <-chan domain.SessionID { return n.updates }
func (n *observedNative) publish(id domain.SessionID, snapshot sessionruntime.NativeSnapshot) {
	n.mu.Lock()
	n.snapshot = snapshot
	n.mu.Unlock()
	n.updates <- id
}

func TestNativeObserverFollowsOnlyVisibleCardAndStopsOnClose(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider", 1)
	native := &observedNative{updates: make(chan domain.SessionID, 8)}
	notifications := make(chan telegramcontroller.Notification, 8)
	c := newController(t, nil, newLockedSessions(ready), nil, notifierFunc(func(_ context.Context, n telegramcontroller.Notification) error { notifications <- n; return nil }), telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native})
	defer c.Close(ctx)
	c.StartNativeObserver()
	c.StartNativeObserver()
	_, err := c.HandleSemanticMessage(ctx, message(980, "/menu"))
	if err != nil {
		t.Fatal(err)
	}
	native.publish(ready.ID(), sessionruntime.NativeSnapshot{Text: "Select model", Hash: strings.Repeat("a", 64), Interactive: true})
	select {
	case n := <-notifications:
		t.Fatalf("menu overwritten: %+v", n)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID()}); err != nil {
		t.Fatal(err)
	}
	native.publish(ready.ID(), sessionruntime.NativeSnapshot{Text: "Choose actual option", Hash: strings.Repeat("b", 64), Interactive: true})
	select {
	case n := <-notifications:
		if n.Kind != telegramcontroller.NotificationNativeScreen || n.SessionID != ready.ID() || strings.Contains(n.Text, "actual option") {
			t.Fatalf("native observation identity/raw payload: %+v", n)
		}
	case <-time.After(time.Second):
		t.Fatal("native picker was not observed")
	}
	r, err := c.ProjectCurrent(ctx, ready.ID())
	if err != nil || r.Surface == nil || r.Surface.Text != "Choose actual option" {
		t.Fatalf("observed picker missing: %+v %v", r, err)
	}
	delivery, cancelDelivery, visible := c.NativeDeliveryContext(ctx, ready.ID())
	defer cancelDelivery()
	if !visible {
		t.Fatal("shown native screen lacks delivery context")
	}
	if _, err := c.HandleSemanticMessage(ctx, message(981, "/menu")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivery.Done():
	case <-time.After(time.Second):
		t.Fatal("menu did not cancel queued native delivery")
	}
	if _, _, visible := c.NativeDeliveryContext(ctx, ready.ID()); visible {
		t.Fatal("menu exposes native delivery context")
	}
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSessions}); err != nil {
		t.Fatal(err)
	}
	native.publish(ready.ID(), sessionruntime.NativeSnapshot{Text: "ordinary terminal", Hash: strings.Repeat("c", 64)})
	select {
	case <-notifications:
	case <-time.After(time.Second):
		t.Fatal("picker close was not observed")
	}
	r, err = c.ProjectCurrent(ctx, ready.ID())
	if err != nil || r.Card == nil {
		t.Fatalf("closed picker did not restore card: %+v %v", r, err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	native.publish(ready.ID(), sessionruntime.NativeSnapshot{Text: "late picker", Hash: strings.Repeat("d", 64), Interactive: true})
	select {
	case n := <-notifications:
		t.Fatalf("observer published after Close: %+v", n)
	case <-time.After(30 * time.Millisecond):
	}
}
