package telegramcontroller_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type persistentControllerLifecycle struct{ aborted, detached atomic.Int32 }

func (*persistentControllerLifecycle) SupportsAttach(domain.Provider) bool { return true }
func (*persistentControllerLifecycle) Attach(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("unexpected attach")
}
func (p *persistentControllerLifecycle) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	p.aborted.Add(1)
	return nil
}
func (p *persistentControllerLifecycle) Detach(_ context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	if request.SessionID == "" || binding.SessionID != "provider-1" {
		return errors.New("detach identity lost")
	}
	p.detached.Add(1)
	return nil
}

func TestControllerShutdownDetachesPersistentAcceptedSessionWithoutAbort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	accepted := make(chan struct{})
	provider := &interactiveSubmitter{submitWithCallbacks: func(ctx context.Context, _ domain.SessionID, _ string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if err := cb.OnAccepted(cb.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		close(accepted)
		<-ctx.Done()
		return sessionruntime.TurnResult{}, ctx.Err()
	}}
	lifecycle := &persistentControllerLifecycle{}
	c := newController(t, creator, newLockedSessions(ready), provider, nil, telegramcontroller.Options{Lifecycle: lifecycle})
	defer c.Close(context.Background())
	mustStatus(t, c, message(70, "/new codex "+ready.Workdir()))
	mustStatus(t, c, message(71, "already accepted"))
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("provider did not accept")
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if lifecycle.aborted.Load() != 0 || lifecycle.detached.Load() != 1 {
		t.Fatalf("shutdown aborted=%d detached=%d; same CLI must survive", lifecycle.aborted.Load(), lifecycle.detached.Load())
	}
	if err := c.Close(ctx); err != nil || lifecycle.detached.Load() != 1 {
		t.Fatal("shutdown repeated detach")
	}
}
