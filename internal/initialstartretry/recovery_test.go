package initialstartretry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/initialstartretry"
	"bria/internal/providerattachport"
)

type cancelBeforeReplaceStore struct {
	session   domain.Session
	cancel    context.CancelFunc
	cancelled bool
}

func (store *cancelBeforeReplaceStore) Load(ctx context.Context, _ domain.SessionID) (domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	return store.session, nil
}

func (store *cancelBeforeReplaceStore) Replace(ctx context.Context, previous, next domain.Session) error {
	store.cancel()
	store.cancelled = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("recovery persistence context is not bounded")
	}
	if !store.session.Equal(previous) {
		return errors.New("recovery persistence conflict")
	}
	store.session = next
	return nil
}

type recoveryStarter struct{ aborts int }

func (*recoveryStarter) Start(_ context.Context, request providerattachport.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{Provider: request.Provider, SessionID: "recovered-provider", Generation: 1}, nil
}

func (starter *recoveryStarter) Abort(context.Context, providerattachport.StartSessionRequest, domain.ProviderBinding) error {
	starter.aborts++
	return nil
}

func TestRecoverUnboundFencesCancellationThatRacesPersistence(t *testing.T) {
	starting, err := domain.NewStartingSession("99999999-9999-4999-8999-999999999999", "intent-recovery-race", "computer", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := starting.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelBeforeReplaceStore{session: awaiting, cancel: cancel}
	starter := &recoveryStarter{}
	result, recoverErr := initialstartretry.RecoverUnbound(ctx, awaiting, store, starter, time.Now)
	if recoverErr != nil || result.Session.Status() != domain.SessionReady || !store.cancelled || starter.aborts != 0 {
		t.Fatalf("RecoverUnbound() = status %q cancelled %t aborts %d error %v", result.Session.Status(), store.cancelled, starter.aborts, recoverErr)
	}
}
