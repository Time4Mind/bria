package telegramcontroller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

type wakingCustody struct{ wake func(domain.SessionID) }

func (w wakingCustody) Accept(context.Context, turnprocessing.SessionInput) (turnprocessing.InputReceipt, error) {
	return turnprocessing.InputReceipt{}, errors.New("unexpected new incoming input")
}
func (w wakingCustody) WakeSession(id domain.SessionID) { w.wake(id) }

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
