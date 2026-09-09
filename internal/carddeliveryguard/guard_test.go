package carddeliveryguard_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/carddeliveryguard"
	"bria/internal/domain"
	"bria/internal/telegramstate"
)

type view struct{ context.Context }

func (v view) NativeDeliveryContext(context.Context, domain.SessionID) (context.Context, context.CancelFunc, bool) {
	return v.Context, func() {}, true
}

func TestScopeSuppressesOnlyNavigationAndStaleDelivery(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	child, cancelView := context.WithCancel(parent)
	defer cancelView()
	scope := carddeliveryguard.Capture(parent, view{child}, "session")
	defer scope.Close()
	if scope.Suppressed(context.Canceled) || scope.Suppressed(errors.New("storage failure")) {
		t.Fatal("live view hid unrelated error")
	}
	cancelView()
	if !scope.Suppressed(context.Canceled) || !scope.Suppressed(carddeliveryguard.ErrNotCurrent) {
		t.Fatal("navigation is not suppressed")
	}
	cancelParent()
	if scope.Suppressed(context.Canceled) || scope.Suppressed(carddeliveryguard.ErrNotCurrent) {
		t.Fatal("service cancellation is not suppression")
	}
}

func TestScopeDoesNotSuppressDistinctShutdownCause(t *testing.T) {
	parent := context.Background()
	child, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	scope := carddeliveryguard.Capture(parent, view{child}, "session")
	defer scope.Close()
	cancel(errors.New("controller shutdown"))
	if scope.Suppressed(context.Canceled) || scope.Suppressed(carddeliveryguard.ErrNotCurrent) {
		t.Fatal("controller shutdown hidden as navigation with live parent")
	}
}

func TestCheckAndTargetUseExactDurableCard(t *testing.T) {
	ctx := context.Background()
	const id domain.SessionID = "session"
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 77}
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(s *telegramstate.State) error {
		s.ActiveSession = id
		return s.SetCard(telegramstate.Card{SessionID: id, Carrier: carrier, Page: telegramstate.Page{Current: 1, Total: 1}, PendingFinalOperations: []string{"a:final", "b:final"}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := carddeliveryguard.Check(ctx, store, id, carrier); !errors.Is(err, carddeliveryguard.ErrNotCurrent) {
		t.Fatalf("pending final admitted routine edit: %v", err)
	}
	for _, test := range []struct {
		op    string
		known bool
		want  string
	}{
		{"a:final", false, "a:final"}, {"b:final", false, "b:final"},
		{"other:final", true, "other:final"}, {"legacy:final", false, ""},
	} {
		if got, err := carddeliveryguard.Target(ctx, store, id, test.op, test.known); err != nil || got != test.want {
			t.Fatalf("target(%s)=%q want=%q err=%v", test.op, got, test.want, err)
		}
	}
	if err := store.Update(ctx, func(s *telegramstate.State) error {
		card, _ := s.Card(id)
		card.PendingFinalOperations = nil
		card.Carrier.MessageID = 88
		return s.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	if err := carddeliveryguard.Check(ctx, store, id, carrier); !errors.Is(err, carddeliveryguard.ErrNotCurrent) {
		t.Fatal("old carrier admitted after final commit")
	}
	carrier.MessageID = 88
	if err := carddeliveryguard.Check(ctx, store, id, carrier); err != nil {
		t.Fatal(err)
	}
	if err := carddeliveryguard.CheckEdit(ctx, nil, id, carrier, false); err != nil {
		t.Fatal("new final send incorrectly fenced")
	}
}
