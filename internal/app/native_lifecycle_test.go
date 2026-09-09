package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

type nativeLifecycle struct {
	*lifecycleStarter
	supported bool
	attach    func(app.StartSessionRequest) (domain.ProviderBinding, error)
	detached  []domain.ProviderBinding
	started   bool
}

func (n *nativeLifecycle) SupportsAttach(domain.Provider) bool { return n.supported }
func (n *nativeLifecycle) Attach(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	return n.attach(request)
}
func (n *nativeLifecycle) Start(ctx context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	n.started = true
	return n.lifecycleStarter.Start(ctx, request)
}
func (n *nativeLifecycle) Detach(ctx context.Context, _ app.StartSessionRequest, binding domain.ProviderBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("detach cleanup is not bounded")
	}
	n.detached = append(n.detached, binding)
	return nil
}

func TestNativeAwaitingCloseDoesNotTreatMissingObserverAsExit(t *testing.T) {
	now := time.Now().UTC()
	ready := readySession(t, now.Add(-time.Hour))
	awaiting, err := ready.AwaitRecoveryAt(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, native := range []bool{true, false} {
		t.Run(map[bool]string{true: "native", false: "stdio"}[native], func(t *testing.T) {
			store := &lifecycleStore{session: awaiting}
			missing := errors.New("session process is not tracked")
			runtime := &nativeLifecycle{lifecycleStarter: &lifecycleStarter{abortErr: missing}, supported: native}
			closer, err := app.NewSessionCloser(store, runtime, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			result, err := closer.Close(context.Background(), awaiting.ID())
			if native {
				target, _ := store.session.RecoveryTarget()
				if !errors.Is(err, missing) || result.Deleted || store.session.Status() != domain.SessionAwaitingRecovery || target != domain.SessionClosing {
					t.Fatalf("detached live CLI was falsely archived: %+v target=%s err=%v", result, target, err)
				}
			} else if err != nil || store.session.Status() != domain.SessionArchived {
				t.Fatalf("stdio close compatibility changed: %+v err=%v", result, err)
			}
		})
	}
}

func TestNativeArchivedResumeUsesExplicitStartAndRollsBackOnlyObserver(t *testing.T) {
	for _, mode := range []string{"managed", "closed-unmanaged", "imported", "managed-dead", "unavailable", "replacement", "persist-failure", "cancelled", "stdio"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC()
			archived := archivedSession(t, now.Add(-time.Hour))
			prior, _ := archived.Binding()
			next := prior
			next.Generation++
			store := &lifecycleStore{session: archived}
			if mode == "persist-failure" {
				store.replaceErr = errors.New("public disk fault")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime := &nativeLifecycle{lifecycleStarter: &lifecycleStarter{}, supported: mode != "stdio"}
			runtime.attach = func(app.StartSessionRequest) (domain.ProviderBinding, error) {
				return domain.ProviderBinding{}, errors.New("explicit archive resume used automatic Attach-only path")
			}
			startError := errors.New("managed terminal cannot be resumed")
			runtime.lifecycleStarter.start = func(request app.StartSessionRequest) (domain.ProviderBinding, error) {
				if request.Mode != app.SessionStartResume || request.SessionID != archived.ID() || request.Provider != archived.Provider() || request.Workdir != archived.Workdir() || request.PriorBinding == nil || *request.PriorBinding != prior {
					t.Fatal("explicit resume lost exact archived identity")
				}
				if mode == "managed-dead" || mode == "unavailable" {
					return domain.ProviderBinding{}, startError
				}
				if mode == "replacement" {
					next.SessionID = "wrong-native-session"
				}
				if mode == "cancelled" {
					cancel()
				}
				return next, nil
			}
			resumer, err := app.NewArchivedSessionResumer(store, runtime, domain.SessionLifetime6Hours, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			result, err := resumer.Resume(ctx, archived.ID())
			if mode == "managed" || mode == "closed-unmanaged" || mode == "imported" || mode == "stdio" {
				binding, _ := result.Binding()
				if err != nil || result.Status() != domain.SessionReady || binding != next || !store.session.Equal(result) {
					t.Fatalf("exact resume failed: %+v err=%v", result, err)
				}
			} else if err == nil || !store.session.Equal(archived) {
				t.Fatalf("failed resume changed archive: %+v err=%v", store.session, err)
			}
			if (mode == "managed-dead" || mode == "unavailable") && !errors.Is(err, startError) {
				t.Fatalf("explicit resume lost underlying refusal: %v", err)
			}
			wantDetach := mode == "replacement" || mode == "persist-failure" || mode == "cancelled"
			if (len(runtime.detached) == 1) != wantDetach || runtime.abortCalls != 0 || !runtime.started {
				t.Fatalf("unsafe cleanup/start: detached=%v abort=%d started=%t", runtime.detached, runtime.abortCalls, runtime.started)
			}
			if wantDetach && runtime.detached[0] != next {
				t.Fatal("rollback detached a different observer generation")
			}
		})
	}
}
