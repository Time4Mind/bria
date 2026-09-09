// Package turnfinalization retries local effects of an already proven terminal.
package turnfinalization

import (
	"bria/internal/domain"
	"bria/internal/finalpersist"
	"context"
	"time"
)

// Retry never invokes a provider. Its caller supplies only one idempotent local
// step for the retained terminal and exact original binding.
func Retry(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding, sessions finalpersist.Sessions, save func(context.Context) error, failed func(uint64, error)) error {
	return finalpersist.Retry(ctx, finalpersist.DefaultPolicy(), func(attempt context.Context) error {
		bounded, cancel := context.WithTimeout(attempt, 2*time.Second)
		defer cancel()
		current, err := sessions.Load(bounded, id)
		if err != nil {
			return err
		}
		actual, bound := current.Binding()
		if current.ID() != id || !bound || actual != binding {
			return finalpersist.ErrSuperseded
		}
		return save(bounded)
	}, failed)
}
