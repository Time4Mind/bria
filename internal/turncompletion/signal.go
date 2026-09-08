// Package turncompletion publishes one eventual terminal result to any number
// of readers independently of an earlier provider-acceptance acknowledgement.
package turncompletion

import (
	"context"
	"errors"
	"sync"
)

// Signal must be constructed with New and must not be copied after use.
type Signal struct {
	once  sync.Once
	done  chan struct{}
	state string
}

func New() *Signal { return &Signal{done: make(chan struct{})} }

func (signal *Signal) Done() <-chan struct{} { return signal.done }

// Resolve publishes the first result before releasing all current/future readers.
func (signal *Signal) Resolve(state string) {
	signal.once.Do(func() {
		signal.state = state
		close(signal.done)
	})
}

// Wait does not consume the result. A published result wins over cancellation;
// cancelling an unresolved wait does not resolve or cancel the shared signal.
func (signal *Signal) Wait(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("completion wait context is required")
	}
	select {
	case <-signal.done:
		return signal.state, nil
	default:
	}
	select {
	case <-signal.done:
		return signal.state, nil
	case <-ctx.Done():
		select {
		case <-signal.done:
			return signal.state, nil
		default:
			return "", ctx.Err()
		}
	}
}
