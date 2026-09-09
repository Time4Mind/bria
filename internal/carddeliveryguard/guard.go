// Package carddeliveryguard gates routine card delivery against view ownership
// and durable final publication without owning projection or transport.
package carddeliveryguard

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

var ErrNotCurrent = errors.New("card delivery no longer owns current carrier")

type Store interface {
	Load(context.Context) (telegramstate.State, error)
}

type Scope struct {
	Context context.Context
	Visible bool
	parent  context.Context
	cancel  context.CancelFunc
}

func Capture(parent context.Context, controller any, id domain.SessionID) Scope {
	s := Scope{Context: parent, Visible: true, parent: parent, cancel: func() {}}
	if view, ok := controller.(interface {
		NativeDeliveryContext(context.Context, domain.SessionID) (context.Context, context.CancelFunc, bool)
	}); ok {
		s.Context, s.cancel, s.Visible = view.NativeDeliveryContext(parent, id)
	} else if view, ok := controller.(interface{ NativeScreenVisible(domain.SessionID) bool }); ok {
		s.Visible = view.NativeScreenVisible(id)
	}
	return s
}

func (s Scope) Close() { s.cancel() }

// Expected view cancellation is suppression; caller/service cancellation and
// unrelated transport/storage errors remain failures.
func (s Scope) Suppressed(err error) bool {
	if cause := context.Cause(s.Context); cause != nil && cause != context.Canceled {
		return false
	}
	return s.parent.Err() == nil && (errors.Is(err, ErrNotCurrent) || errors.Is(err, context.Canceled) && s.Context.Err() != nil)
}

func Check(ctx context.Context, store Store, id domain.SessionID, carrier telegramstate.Carrier) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := store.Load(ctx)
	if err != nil {
		return err
	}
	card, found := state.Card(id)
	if !found || state.ActiveSession != id || card.Carrier != carrier || len(card.PendingFinalOperations) != 0 {
		return ErrNotCurrent
	}
	return nil
}

func CheckEdit(ctx context.Context, store Store, id domain.SessionID, carrier telegramstate.Carrier, edit bool) error {
	if !edit {
		return nil
	}
	return Check(ctx, store, id, carrier)
}

// Target requires exact selection when either the projected history or durable
// pending publication identifies this as a typed final. Old unbound fixtures
// retain their legacy selection contract.
func Target(ctx context.Context, store Store, id domain.SessionID, operation string, knownFinal bool) (string, error) {
	if knownFinal {
		return operation, nil
	}
	if store == nil {
		return "", nil
	}
	state, err := store.Load(ctx)
	if err != nil {
		return "", err
	}
	card, _ := state.Card(id)
	for _, pending := range card.PendingFinalOperations {
		if pending == operation {
			return operation, nil
		}
	}
	return "", nil
}
