package singlemachinecomposition

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
)

type approvalObserverProbe struct {
	recoveryWaiting <-chan struct{}
	approved        chan<- struct{}
}

func (p approvalObserverProbe) StartNativeObserver() {
	go func() {
		<-p.recoveryWaiting
		p.approved <- struct{}{}
	}()
}

type approvalBlockedRecovery struct {
	waiting  chan<- struct{}
	approved <-chan struct{}
	err      error
}

func (r approvalBlockedRecovery) RecoverStartup(ctx context.Context) (app.SessionRecoveryResult, error) {
	r.waiting <- struct{}{}
	select {
	case <-ctx.Done():
		return app.SessionRecoveryResult{}, ctx.Err()
	case <-r.approved:
		return app.SessionRecoveryResult{}, r.err
	}
}

// A recovered Codex turn may already be waiting for a native approval. This
// seam deadlocks if the observer is started only after RecoverStartup returns.
func TestNativeObserverUnblocksApprovalDuringStartupRecovery(t *testing.T) {
	waiting := make(chan struct{}, 1)
	approved := make(chan struct{}, 1)
	wantErr := errors.New("recovery stopped after approval")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := recoverStartupWithNativeObserver(ctx,
		approvalObserverProbe{recoveryWaiting: waiting, approved: approved},
		approvalBlockedRecovery{waiting: waiting, approved: approved, err: wantErr},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("startup recovery error = %v, want post-approval error", err)
	}
}
