package nativerecoverycomposition

import (
	"bria/internal/recoveryruntime"
	"bria/internal/sessionruntime"
	"context"
)

// Runtime retains all starter capabilities while choosing native receipt reads.
type Runtime struct {
	*sessionruntime.Starter
	Reader sessionruntime.AcceptedTurnReader
}

func (runtime Runtime) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	if runtime.Reader == nil {
		return sessionruntime.AcceptedTurnReconciliation{}, recoveryruntime.ErrUnavailable
	}
	return runtime.Reader.ReadAcceptedTurns(ctx, request)
}
