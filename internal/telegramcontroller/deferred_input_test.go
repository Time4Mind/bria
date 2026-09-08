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

func TestLeasedInputDefersAfterPriorFinalizationFails(t *testing.T) {
	for _, mode := range []string{"automatic", "manual", "during-cleanup"} {
		t.Run(mode, func(t *testing.T) { testDeferredInputRecovery(t, mode) })
	}
}

func testDeferredInputRecovery(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	store := newLockedSessions(ready)
	history := &deferredHistory{entered: make(chan struct{}), release: make(chan struct{})}
	defer history.unblock()
	sent := make(chan string, 4)
	provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		sent <- text
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed}, sessionruntime.ErrTurnFailed
	}}
	lifecycle := turnLifecycleFunc{
		start: func(ctx context.Context, id domain.SessionID) (domain.Session, error) {
			current, err := store.Load(ctx, id)
			if err != nil {
				return domain.Session{}, err
			}
			running, err := current.StartWork(time.Now())
			if err == nil {
				store.Set(running)
			}
			return running, err
		},
		finish: func(ctx context.Context, id domain.SessionID) (domain.Session, bool, error) {
			current, err := store.Load(ctx, id)
			if err != nil {
				return domain.Session{}, false, err
			}
			finished, err := current.FinishWork(time.Now())
			if err == nil {
				store.Set(finished)
			}
			return finished, false, err
		},
	}
	recover := deferredRecoverer(func(ctx context.Context, id domain.SessionID) (domain.Session, error) {
		current, err := store.Load(ctx, id)
		if err != nil {
			return domain.Session{}, err
		}
		binding, _ := current.Binding()
		binding.Generation++
		recovered, err := current.Recovered(binding, time.Now())
		if err == nil {
			store.Set(recovered)
		}
		return recovered, err
	})
	recoveryDone := make(chan struct{}, 1)
	controller := newController(t, nil, store, provider, notifierFunc(func(_ context.Context, n telegramcontroller.Notification) error {
		if n.Text == "Сессия восстановлена без повторной отправки запроса." {
			recoveryDone <- struct{}{}
		}
		return nil
	}), telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: history, TurnLifecycle: lifecycle, Recoverer: recover})
	commitRecovery := func() {
		current, err := store.Load(ctx, ready.ID())
		if err != nil {
			t.Fatal(err)
		}
		awaiting, err := current.AwaitRecoveryAt(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		store.Set(awaiting)
		if mode == "manual" {
			if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticResume, SessionID: ready.ID()}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-recoveryDone:
			case <-ctx.Done():
				t.Fatal("manual recovery did not finish")
			}
		} else {
			if _, err := recover(ctx, ready.ID()); err != nil {
				t.Fatal(err)
			}
			controller.RefreshRecoveryCard(ctx, ready.ID())
		}
	}
	defer func() { history.unblock(); _ = controller.Close(context.Background()) }()
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 4)
	bCompleted := make(chan telegramcontroller.DurableInputProcessReceipt, 4)
	callbacks := telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
		OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
			if receipt.MessageID == "A" {
				completed <- receipt
			} else {
				bCompleted <- receipt
			}
			return nil
		},
	}
	input := func(id string, sequence uint64) telegramcontroller.DurableLeasedInput {
		return telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: id, Sequence: sequence, Payload: []byte(id)}
	}
	if receipt, err := controller.ProcessDurableInput(ctx, input("A", 1), callbacks); err != nil || !receipt.Accepted {
		t.Fatalf("A=%+v err=%v", receipt, err)
	}
	select {
	case <-history.entered:
	case <-ctx.Done():
		t.Fatal("A did not enter finalization")
	}
	if got := <-sent; got != "A" {
		t.Fatalf("first provider input=%q", got)
	}
	controller.RefreshRecoveryCard(ctx, ready.ID())
	if mode == "during-cleanup" {
		commitRecovery()
	}
	type result struct {
		receipt telegramcontroller.DurableInputProcessReceipt
		err     error
	}
	returned := make(chan result, 1)
	go func() {
		receipt, err := controller.ProcessDurableInput(ctx, input("B", 2), callbacks)
		returned <- result{receipt, err}
	}()
	select {
	case got := <-returned:
		t.Fatalf("B did not wait for A finalization: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	history.unblock()
	select {
	case got := <-returned:
		if mode == "during-cleanup" {
			if !got.receipt.Accepted || got.err != nil {
				t.Fatalf("pending recovery did not release B after settlement: %+v %v", got.receipt, got.err)
			}
		} else if got.receipt.Accepted || !errors.Is(got.err, turnprocessing.ErrInputDeferred) || got.receipt.SessionID != ready.ID() || got.receipt.MessageID != "B" || got.receipt.Sequence != 2 {
			t.Fatalf("definitely unsent B must defer with exact receipt: %+v err=%v", got.receipt, got.err)
		}
	case <-ctx.Done():
		t.Fatal("B did not finish deferring")
	}
	if mode != "during-cleanup" {
		select {
		case text := <-sent:
			t.Fatalf("blocked input reached provider: %q", text)
		default:
		}
	}
	select {
	case receipt := <-completed:
		if receipt.MessageID != "A" || receipt.Completion != telegramcontroller.DurableInputCompletion("awaiting_recovery") {
			t.Fatalf("A completion=%+v", receipt)
		}
	case <-ctx.Done():
		t.Fatal("missing A completion")
	}
	if mode != "during-cleanup" {
		if receipt, err := controller.ProcessDurableInput(ctx, input("B", 2), callbacks); receipt.Accepted || !errors.Is(err, turnprocessing.ErrInputDeferred) {
			t.Fatalf("settled Unknown admitted B before recovery: %+v %v", receipt, err)
		}
		commitRecovery()
		if receipt, err := controller.ProcessDurableInput(ctx, input("B", 2), callbacks); err != nil || !receipt.Accepted {
			t.Fatalf("committed recovery did not release stale finalization guard: %+v %v", receipt, err)
		}
	}
	select {
	case text := <-sent:
		if text != "B" {
			t.Fatalf("replayed input: %q", text)
		}
	case <-ctx.Done():
		t.Fatal("B did not reach provider after recovery")
	}
	select {
	case receipt := <-bCompleted:
		if receipt.MessageID != "B" || receipt.Completion != telegramcontroller.DurableInputCompletion("awaiting_recovery") {
			t.Fatalf("new generation completion=%+v", receipt)
		}
	case <-ctx.Done():
		t.Fatal("B did not finish")
	}
	// A delayed session-ID-only callback must not erase the new Unknown.
	controller.RefreshRecoveryCard(ctx, ready.ID())
	if receipt, err := controller.ProcessDurableInput(ctx, input("C", 3), callbacks); receipt.Accepted || !errors.Is(err, turnprocessing.ErrInputDeferred) {
		t.Fatalf("delayed recovery refresh cleared newer Unknown: %+v %v", receipt, err)
	}
	select {
	case text := <-sent:
		t.Fatalf("unknown generation sent another input: %q", text)
	default:
	}
}

type deferredRecoverer func(context.Context, domain.SessionID) (domain.Session, error)

func (f deferredRecoverer) RecoverSession(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return f(ctx, id)
}

type deferredHistory struct {
	entered, release chan struct{}
	once             sync.Once
	unblockOnce      sync.Once
}

func (h *deferredHistory) unblock()                                               { h.unblockOnce.Do(func() { close(h.release) }) }
func (*deferredHistory) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (*deferredHistory) LoadCardHistory(context.Context, domain.SessionID) ([]string, error) {
	return nil, nil
}
func (h *deferredHistory) AppendCardHistory(context.Context, domain.SessionID, string) error {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return errors.New("synthetic final history write failure")
}
