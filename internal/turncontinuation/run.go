package turncontinuation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

// Run is protected by its caller's worker lock, including Done.
type Run struct {
	Binding domain.ProviderBinding
	key     [32]byte
	Done    bool
}

func Claim(prior *Run, binding domain.ProviderBinding, groups []Group, active bool) (*Run, bool, error) {
	h := sha256.New()
	for _, g := range groups {
		for _, m := range g.Members {
			fmt.Fprintf(h, "%d:%s%d:%s%d;", len(m.TurnID), m.TurnID, len(m.Input.MessageID), m.Input.MessageID, m.Input.Sequence)
		}
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	if prior != nil && prior.Binding == binding && prior.key == key {
		return prior, false, nil
	}
	if prior != nil && !prior.Done || active {
		return nil, false, errors.New("accepted continuation already has an active observer")
	}
	return &Run{Binding: binding, key: key}, true, nil
}

// Execute never advances past an unresolved observation or failed member commit.
// A group has one observer/final producer but exact individual custody receipts.
func Execute(ctx context.Context, groups []Group, observe func(Member) turnprocessing.DurableInputCompletion, commit func(Member, turnprocessing.DurableInputCompletion) error, finish func() error) error {
	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		outcome := observe(g.Members[0])
		var failure error
		for _, m := range g.Members {
			failure = errors.Join(failure, commit(m, outcome))
		}
		if failure != nil {
			return failure
		}
		if outcome != turnprocessing.DurableInputSucceeded && outcome != turnprocessing.DurableInputTerminalFailed {
			return errors.New("accepted continuation remains unresolved")
		}
	}
	return finish()
}

type Observer interface {
	ObserveAcceptedWithCallbacks(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error)
}

func Observe(ctx context.Context, observer Observer, binding domain.ProviderBinding, request turnprocessing.Request, callbacks turnprocessing.Callbacks) (turnprocessing.Execution, error) {
	if observer == nil {
		return turnprocessing.Execution{Accepted: true}, errors.New("accepted turn observation is unavailable")
	}
	result, err := observer.ObserveAcceptedWithCallbacks(ctx, request.SessionID, binding, sessionruntime.TurnCallbacks{MessageID: request.MessageID, OnEvent: callbacks.OnEvent, OnAccepted: func(message string) error {
		if message != request.MessageID {
			return errors.New("observer acknowledged another accepted message")
		}
		return nil
	}})
	return turnprocessing.Execution{Result: result, Accepted: true, StreamedEvents: true}, err
}
