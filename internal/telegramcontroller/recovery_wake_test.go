package telegramcontroller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

type wakingCustody struct{ wake func(domain.SessionID) }

func (w wakingCustody) Accept(context.Context, turnprocessing.SessionInput) (turnprocessing.InputReceipt, error) {
	return turnprocessing.InputReceipt{}, errors.New("unexpected new incoming input")
}
func (w wakingCustody) WakeSession(id domain.SessionID) { w.wake(id) }

type startingWakingCustody struct{ wake chan domain.SessionID }

func (w startingWakingCustody) Accept(_ context.Context, input turnprocessing.SessionInput) (turnprocessing.InputReceipt, error) {
	return turnprocessing.InputReceipt{Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
}
func (w startingWakingCustody) WakeSession(id domain.SessionID) { w.wake <- id }

func TestAsyncCreateWakesInputQueuedWhileStartingAfterReady(t *testing.T) {
	workdir := t.TempDir()
	starting, err := domain.NewStartingSession("bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb", "telegram-update:9100", "local", domain.ProviderCodex, workdir)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-ready", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	store := newLockedSessions(starting)
	outcomes := make(chan telegramcontroller.SessionStartOutcome, 1)
	wakes := make(chan domain.SessionID, 1)
	controller := newController(t, nil, store, submitterFunc(nil), nil, telegramcontroller.Options{
		AsyncCreator: asyncCreatorFunc(func(context.Context, app.ConfirmedSessionIntent) (telegramcontroller.PendingSessionStart, error) {
			return telegramcontroller.PendingSessionStart{Session: starting, Outcome: outcomes}, nil
		}),
		DurableInput: startingWakingCustody{wake: wakes},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(9100, "/new codex "+workdir))
	mustStatus(t, controller, message(9101, "queued while starting"))
	store.Set(ready)
	outcomes <- telegramcontroller.SessionStartOutcome{Session: ready}
	close(outcomes)
	select {
	case id := <-wakes:
		if id != ready.ID() {
			t.Fatalf("woke session %q, want %q", id, ready.ID())
		}
	case <-time.After(time.Second):
		t.Fatal("ready async-created session did not wake its queued durable input")
	}
}

func TestDurableInputDefersAcrossReadyStoreToLiveControllerGap(t *testing.T) {
	ready := readySession(t, "cccccccc-cccc-4ccc-9ccc-cccccccccccc", domain.ProviderCodex, t.TempDir(), "provider-ready", 1)
	provider := &interactiveSubmitter{submitWithCallbacks: func(context.Context, domain.SessionID, string, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		t.Fatal("transient not-live input reached provider")
		return sessionruntime.TurnResult{}, nil
	}}
	controller := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	receipt, err := controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
		SessionID: ready.ID(), MessageID: "queued", Sequence: 1, Payload: []byte("queued"),
	}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil }})
	if receipt.Accepted || receipt.SessionID != ready.ID() || receipt.MessageID != "queued" || receipt.Sequence != 1 || !errors.Is(err, turnprocessing.ErrInputDeferred) {
		t.Fatalf("transient live gap = (%#v, %v), want exact deferred receipt", receipt, err)
	}
}

func TestRecoveryWakeCanProcessPendingInputOnlyAfterLiveReady(t *testing.T) {
	for _, path := range []string{"resume", "async-resume", "refresh", "manual"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			initial := archivedSession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
			resuming, err := initial.BeginResume(initial.StateChangedAt().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			ready, err := resuming.ResumeReady(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-a", Generation: 2}, resuming.StateChangedAt().Add(time.Minute), domain.SessionLifetimeNever)
			if err != nil {
				t.Fatal(err)
			}
			if path == "manual" || path == "refresh" {
				initial, err = ready.AwaitRecoveryAt(ready.StateChangedAt().Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
			}
			store := newLockedSessions(initial)
			release := make(chan struct{})
			processed := make(chan error, 4)
			var c *telegramcontroller.Controller
			waker := wakingCustody{wake: func(id domain.SessionID) {
				if id != ready.ID() {
					processed <- errors.New("woke different session")
					return
				}
				receipt, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: id, MessageID: "B", Sequence: 2, Payload: []byte("queued successor")}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil }})
				if err == nil && !receipt.Accepted {
					err = errors.New("B not accepted after wake")
				}
				processed <- err
			}}
			outcomes := make(chan telegramcontroller.SessionStartOutcome, 1)
			options := telegramcontroller.Options{DurableInput: waker,
				ArchivedResumer: archivedResumerFunc(func(context.Context, domain.SessionID) (domain.Session, error) { store.Set(ready); return ready, nil }),
				Recoverer:       deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { store.Set(ready); return ready, nil }),
			}
			if path == "async-resume" {
				options.AsyncResumer = asyncResumerFunc(func(context.Context, domain.SessionID) (telegramcontroller.PendingSessionStart, error) {
					store.Set(resuming)
					return telegramcontroller.PendingSessionStart{Session: resuming, Outcome: outcomes}, nil
				})
			}
			c = newController(t, nil, store, terminalFailureProvider{release: release}, nil, options)
			defer func() { close(release); _ = c.Close(context.Background()) }()
			switch path {
			case "resume", "async-resume":
				_, err = c.ResumeArchived(ctx, initial.ID())
				if path == "async-resume" {
					select {
					case <-processed:
						t.Fatal("woke before resumed ready")
					default:
					}
					store.Set(ready)
					outcomes <- telegramcontroller.SessionStartOutcome{Session: ready}
				}
			case "refresh":
				c.RefreshRecoveryCard(ctx, initial.ID())
				select {
				case <-processed:
					t.Fatal("woke unresolved recovery")
				default:
				}
				store.Set(ready)
				c.RefreshRecoveryCard(ctx, initial.ID())
			case "manual":
				_, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticResume, SessionID: initial.ID()})
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-processed:
				if err != nil {
					t.Fatalf("wake preceded usable live state: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("committed ready stranded B without another incoming wake")
			}
		})
	}
}
