package supervisioncomposition_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/supervisioncomposition"
)

type fallbackRecordingRuntime struct {
	runtimeStub
	recordErr error
}

func (runtime *fallbackRecordingRuntime) ConfirmPersistedExit(request app.StartSessionRequest, binding domain.ProviderBinding) error {
	if runtime.recordErr != nil {
		return runtime.recordErr
	}
	return runtime.runtimeStub.ConfirmPersistedExit(request, binding)
}

func TestSafeFallbackPreservesStartupFences(t *testing.T) {
	for _, scenario := range []string{"remote", "hazardous", "recording_failure"} {
		t.Run(scenario, func(t *testing.T) {
			computer := domain.ComputerID("local")
			if scenario == "remote" {
				computer = "other"
			}
			at := time.Now().UTC()
			session, err := domain.NewStartingSessionAt("fallback-fence", "fallback-fence-intent", computer, domain.ProviderCodex, t.TempDir(), at, domain.SessionLifetimeNever)
			if err != nil {
				t.Fatal(err)
			}
			session, err = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "bound-thread", Generation: 1}, at)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "hazardous" {
				session, err = session.StartWork(at)
				if err != nil {
					t.Fatal(err)
				}
			}
			store := &memoryStore{session: session}
			runtime := &fallbackRecordingRuntime{}
			var wantErr error
			if scenario == "recording_failure" {
				wantErr = errors.New("exit recording failed")
				runtime.recordErr = wantErr
			} else if scenario == "hazardous" {
				wantErr = sessionsupervisor.ErrReconciliationRequired
			}
			result, err := supervisioncomposition.RecoverSafeFallback(context.Background(), "local", store, runtime)
			if !errors.Is(err, wantErr) {
				t.Fatalf("fallback error = %v, want %v", err, wantErr)
			}
			if scenario == "remote" && result.SkippedRemote != 1 {
				t.Fatalf("remote result = %#v", result)
			}
			starts, _ := runtime.counts()
			if starts != 0 || runtime.confirmations() != 0 || !store.session.Equal(session) {
				t.Fatal("fenced startup changed a session or started/recorded a process")
			}
		})
	}
}

func TestFallbackUnavailableAdapterChild(t *testing.T) {
	if os.Getenv("BRIA_FALLBACK_EXIT_CHILD") != "1" {
		return
	}
	os.Exit(1)
}

func TestSafeFallbackFailedResumeStillAllowsOwnerClose(t *testing.T) {
	for _, closing := range []bool{false, true} {
		name := "ready"
		if closing {
			name = "closing"
		}
		t.Run(name, func(t *testing.T) {
			at := time.Now().UTC()
			starting, err := domain.NewStartingSessionAt("fallback-close", "fallback-intent", "local", domain.ProviderCodex, t.TempDir(), at, domain.SessionLifetimeNever)
			if err != nil {
				t.Fatal(err)
			}
			prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "prior-thread", Generation: 3}
			ready, err := starting.ReadyAt(prior, at)
			if err != nil {
				t.Fatal(err)
			}
			if closing {
				ready, err = ready.BeginClose(at)
				if err != nil {
					t.Fatal(err)
				}
			}
			awaiting, err := ready.AwaitRecoveryAt(at)
			if err != nil {
				t.Fatal(err)
			}
			store := &memoryStore{session: awaiting}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {Path: executable, Args: []string{"-test.run=^TestFallbackUnavailableAdapterChild$"}, Env: []string{"BRIA_FALLBACK_EXIT_CHILD=1"}},
			}, sessionruntime.Options{HandshakeTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := any(starter).(sessionruntime.AcceptedTurnReader); ok {
				t.Fatal("fixture unexpectedly provides discovery history")
			}
			recovered, err := supervisioncomposition.RecoverSafeFallback(context.Background(), "local", store, starter)
			if err != nil || recovered.Awaiting != 1 {
				t.Fatalf("fallback = %#v, %v", recovered, err)
			}
			closer, err := app.NewSessionCloser(store, starter, func() time.Time { return at.Add(time.Minute) })
			if err != nil {
				t.Fatal(err)
			}
			result, err := closer.Close(context.Background(), awaiting.ID())
			if err != nil {
				t.Fatalf("owner close after no-discovery restart: %v", err)
			}
			if result.Session.Status() != domain.SessionArchived || store.session.Status() != domain.SessionArchived {
				t.Fatal("owner close did not persist archived session")
			}
		})
	}
}
