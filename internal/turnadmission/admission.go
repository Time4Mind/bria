// Package turnadmission gates new roots on settlement of all prior input commits.
package turnadmission

import (
	"context"
	"errors"
	"sync"
)

var ErrCommitFailed = errors.New("turn admission commit failed")

// Admission must be constructed with NewAdmission and must not be copied.
type Admission struct {
	mu      sync.Mutex
	done    chan struct{}
	pending int
	sealed  bool
	failed  bool
	root    Ticket
}

// Ticket must not be copied; its first Finish settles one registered commit.
type Ticket struct {
	admission *Admission
	finished  bool
}

func NewAdmission() *Admission {
	a := &Admission{done: make(chan struct{}), pending: 1}
	a.root.admission = a
	return a
}

// RegisterSteer must succeed before the caller hands input to the provider.
func (a *Admission) RegisterSteer() (*Ticket, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sealed {
		return nil, false
	}
	a.pending++
	return &Ticket{admission: a}, true
}

func (a *Admission) Seal() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sealed {
		return
	}
	a.sealed = true
	a.settleLocked()
}

func (a *Admission) FinishRoot(err error) { a.root.Finish(err) }

func (t *Ticket) Finish(err error) {
	a := t.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	if t.finished {
		return
	}
	t.finished = true
	a.failed = a.failed || err != nil
	a.pending--
	a.settleLocked()
}

func (a *Admission) settleLocked() {
	if a.sealed && a.pending == 0 {
		close(a.done)
	}
}

// Wait cancels only this waiter. A published settlement wins over cancellation.
func (a *Admission) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("admission wait context is required")
	}
	select {
	case <-a.done:
		return a.result()
	default:
	}
	select {
	case <-a.done:
		return a.result()
	case <-ctx.Done():
		select {
		case <-a.done:
			return a.result()
		default:
			return ctx.Err()
		}
	}
}

// Only read after done closes: no first Finish can mutate failed afterwards.
func (a *Admission) result() error {
	if a.failed {
		return ErrCommitFailed
	}
	return nil
}
