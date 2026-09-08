package telegramcontroller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

func TestNextInputWaitsForMainAndSteerJournalCommit(t *testing.T) {
	for _, blocked := range []string{"A", "steer"} {
		for _, fail := range []bool{false, true} {
			name := blocked + "/success"
			if fail {
				name = blocked + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
				finishProvider, finishCommit, commitEntered := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var providerOnce, commitOnce sync.Once
				release := func() {
					providerOnce.Do(func() { close(finishProvider) })
					commitOnce.Do(func() { close(finishCommit) })
				}
				store := newLockedSessions(ready)
				controller := newController(t, nil, store, terminalFailureProvider{release: finishProvider, status: sessionruntime.StatusFailed}, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}})
				defer func() { release(); _ = controller.Close(context.Background()) }()
				callbacks := telegramcontroller.DurableInputCallbacks{
					OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
					OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
						if receipt.MessageID == blocked {
							close(commitEntered)
							<-finishCommit
							if fail {
								return errors.New("synthetic exact completion commit failure")
							}
						}
						return nil
					},
				}
				input := func(id string, seq uint64) telegramcontroller.DurableLeasedInput {
					return telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: id, Sequence: seq, Payload: []byte(id)}
				}
				for index, id := range []string{"A", "steer"} {
					receipt, err := controller.ProcessDurableInput(ctx, input(id, uint64(index+1)), callbacks)
					if err != nil || !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputPending {
						t.Fatalf("early %s=%+v %v", id, receipt, err)
					}
				}
				providerOnce.Do(func() { close(finishProvider) })
				select {
				case <-commitEntered:
				case <-ctx.Done():
					t.Fatal("journal callback was not entered")
				}
				type result struct {
					receipt telegramcontroller.DurableInputProcessReceipt
					err     error
				}
				returned := make(chan result, 1)
				go func() {
					receipt, err := controller.ProcessDurableInput(ctx, input("B", 3), callbacks)
					returned <- result{receipt, err}
				}()
				select {
				case got := <-returned:
					t.Fatalf("B crossed uncommitted %s outcome: %+v %v", blocked, got.receipt, got.err)
				case <-time.After(20 * time.Millisecond):
				}
				commitOnce.Do(func() { close(finishCommit) })
				select {
				case got := <-returned:
					if fail {
						if got.receipt.Accepted || !errors.Is(got.err, turnprocessing.ErrInputDeferred) || got.receipt.MessageID != "B" || got.receipt.Sequence != 3 {
							t.Fatalf("failed commit must defer exact unsent B: %+v %v", got.receipt, got.err)
						}
					} else if !got.receipt.Accepted || got.err != nil {
						t.Fatalf("committed result did not release B: %+v %v", got.receipt, got.err)
					}
				case <-ctx.Done():
					t.Fatal("B did not finish after commit settled")
				}
				if fail {
					controller.RefreshRecoveryCard(ctx, ready.ID())
					if receipt, err := controller.ProcessDurableInput(ctx, input("B", 3), callbacks); receipt.Accepted || !errors.Is(err, turnprocessing.ErrInputDeferred) {
						t.Fatalf("same-generation refresh erased failed commit: %+v %v", receipt, err)
					}
					awaiting, err := ready.AwaitRecoveryAt(time.Now())
					if err != nil {
						t.Fatal(err)
					}
					binding, _ := ready.Binding()
					binding.Generation++
					recovered, err := awaiting.Recovered(binding, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					store.Set(recovered)
					controller.RefreshRecoveryCard(ctx, ready.ID())
					if receipt, err := controller.ProcessDurableInput(ctx, input("B", 3), callbacks); !receipt.Accepted || err != nil {
						t.Fatalf("exact newer recovery did not supersede failed admission: %+v %v", receipt, err)
					}
				}
			})
		}
	}
}

func TestAdmissionIncludesSteerWhoseAcceptanceIsStillInFlight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	rootDone, attempt, acceptSteer := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var rootOnce, steerOnce sync.Once
	release := func() { rootOnce.Do(func() { close(rootDone) }); steerOnce.Do(func() { close(acceptSteer) }) }
	provider := delayedAdmissionSteer{terminalFailureProvider: terminalFailureProvider{release: rootDone, status: sessionruntime.StatusFailed}, attempt: attempt, accept: acceptSteer}
	controller := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}})
	defer func() { release(); _ = controller.Close(context.Background()) }()
	rootCommitted := make(chan struct{})
	callbacks := telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
		OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
			if receipt.MessageID == "A" {
				close(rootCommitted)
				return nil
			}
			return errors.New("synthetic steer completion failure")
		},
	}
	input := func(id string, seq uint64) telegramcontroller.DurableLeasedInput {
		return telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: id, Sequence: seq, Payload: []byte(id)}
	}
	if receipt, err := controller.ProcessDurableInput(ctx, input("A", 1), callbacks); err != nil || !receipt.Accepted {
		t.Fatalf("root=%+v %v", receipt, err)
	}
	steerDone := make(chan error, 1)
	go func() { _, err := controller.ProcessDurableInput(ctx, input("steer", 2), callbacks); steerDone <- err }()
	select {
	case <-attempt:
	case <-ctx.Done():
		t.Fatal("steer did not enter provider attempt")
	}
	rootOnce.Do(func() { close(rootDone) })
	select {
	case <-rootCommitted:
	case <-ctx.Done():
		t.Fatal("root did not settle")
	}
	type result struct {
		receipt telegramcontroller.DurableInputProcessReceipt
		err     error
	}
	returned := make(chan result, 1)
	go func() {
		receipt, err := controller.ProcessDurableInput(ctx, input("B", 3), callbacks)
		returned <- result{receipt, err}
	}()
	select {
	case got := <-returned:
		t.Fatalf("B ignored in-flight steer acceptance: %+v %v", got.receipt, got.err)
	case <-time.After(20 * time.Millisecond):
	}
	steerOnce.Do(func() { close(acceptSteer) })
	select {
	case err := <-steerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("steer did not finish acceptance")
	}
	select {
	case got := <-returned:
		if got.receipt.Accepted || !errors.Is(got.err, turnprocessing.ErrInputDeferred) {
			t.Fatalf("late steer commit failure unblocked B: %+v %v", got.receipt, got.err)
		}
	case <-ctx.Done():
		t.Fatal("admission did not settle")
	}
}

type delayedAdmissionSteer struct {
	terminalFailureProvider
	attempt chan struct{}
	accept  <-chan struct{}
}

func (p delayedAdmissionSteer) SubmitCurrentWithCallbacks(ctx context.Context, _ domain.SessionID, _ sessionruntime.StructuredInput, callbacks sessionruntime.TurnCallbacks) error {
	close(p.attempt)
	select {
	case <-p.accept:
		return callbacks.OnAccepted(callbacks.MessageID)
	case <-ctx.Done():
		return ctx.Err()
	}
}
