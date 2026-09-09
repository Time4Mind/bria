// Package viewdeliverycontext joins caller, view and service lifetimes without
// confusing deliberate view replacement with service shutdown.
package viewdeliverycontext

import (
	"context"
	"errors"
)

var errRootStopped = errors.New("view delivery root stopped")

// Renew cancels the previous view and creates a replacement only when enabled.
func Renew(root context.Context, previous context.CancelFunc, enabled bool) (context.Context, context.CancelFunc) {
	if previous != nil {
		previous()
	}
	if enabled {
		return context.WithCancel(root)
	}
	return nil, nil
}

// Capture preserves caller values and cancellation. Navigation has the exact
// context.Canceled cause; service shutdown has a distinct payload-free cause.
func Capture(parent, view, root context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	observe := func() {
		if root.Err() != nil {
			cancel(errRootStopped)
		} else if view.Err() != nil {
			cancel(context.Canceled)
		}
	}
	stopView := context.AfterFunc(view, observe)
	stopRoot := context.AfterFunc(root, observe)
	observe()
	return observedContext{Context: ctx, observe: observe}, func() { stopView(); stopRoot(); cancel(nil) }
}

// Transport may check a context before AfterFunc's goroutine has run. Observe
// synchronously at that boundary as well as waking existing Done listeners.
type observedContext struct {
	context.Context
	observe func()
}

func (c observedContext) Err() error {
	c.observe()
	return c.Context.Err()
}

func (c observedContext) Done() <-chan struct{} {
	c.observe()
	return c.Context.Done()
}

func (c observedContext) Value(key any) any {
	c.observe()
	return c.Context.Value(key)
}
