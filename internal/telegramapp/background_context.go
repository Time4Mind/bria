package telegramapp

import "context"

// detachedOperationContext lets bounded background work survive the callback
// response while retaining transport-neutral ingress values for diagnostics.
func detachedOperationContext(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}
