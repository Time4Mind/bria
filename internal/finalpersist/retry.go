// Package finalpersist retries only a caller-bound final persistence operation.
package finalpersist

import (
	"context"
	"errors"
	"time"
)

var ErrSuperseded = errors.New("final persistence binding superseded")

type Policy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

func DefaultPolicy() Policy { return Policy{InitialDelay: time.Second, MaxDelay: 30 * time.Second} }

// Retry runs synchronously; callbacks must return and the first save must bound
// its own uncancelled drain. Observers must not log raw storage errors.
func Retry(ctx context.Context, policy Policy, save func(context.Context) error, onFailure func(uint64, error)) error {
	if ctx == nil || save == nil || policy.InitialDelay <= 0 || policy.MaxDelay < policy.InitialDelay {
		return errors.New("invalid final persistence retry configuration")
	}
	delay := policy.InitialDelay
	for attempt := uint64(1); ; attempt++ {
		saveCtx := ctx
		if attempt == 1 {
			saveCtx = context.WithoutCancel(ctx)
		} else if err := ctx.Err(); err != nil {
			return err
		}
		err := save(saveCtx)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrSuperseded) {
			return err
		}
		if onFailure != nil {
			onFailure(attempt, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		// Compare before multiplying, including for near-MaxInt64 durations.
		if delay > policy.MaxDelay/2 {
			delay = policy.MaxDelay
		} else {
			delay *= 2
		}
	}
}
