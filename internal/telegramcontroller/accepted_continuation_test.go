package telegramcontroller_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turncontinuation"
	"bria/internal/turnprocessing"
)

type acceptedObserver struct {
	observe func(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error)
	submits atomic.Int32
}

func TestAcceptedContinuationBatchObservesRootSteerOnceAndKeepsRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := running.Binding()
	sessions := newLockedSessions(running)
	observed := make(chan string, 4)
	release := make(chan struct{})
	committed := make(chan string, 4)
	var finishes atomic.Int32
	provider := &acceptedObserver{observe: func(ctx context.Context, _ domain.SessionID, _ domain.ProviderBinding, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		observed <- cb.MessageID
		if cb.MessageID == "next" {
			select {
			case <-release:
			case <-ctx.Done():
				return sessionruntime.TurnResult{}, ctx.Err()
			}
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "final " + cb.MessageID}, nil
	}}
	c := newController(t, nil, sessions, provider, nil, telegramcontroller.Options{UIState: &retryFinalState{attempt: make(chan struct{}, 8)}, AcceptedObserver: provider, TurnLifecycle: turnLifecycleFunc{finish: func(context.Context, domain.SessionID) (domain.Session, bool, error) {
		finishes.Add(1)
		finished, err := running.FinishWork(time.Now())
		sessions.Set(finished)
		return finished, false, err
	}}})
	defer c.Close(context.Background())
	batch, ok := any(c).(interface {
		ContinueAcceptedBatch(context.Context, domain.ProviderBinding, []turncontinuation.Member, func()) error
	})
	if !ok {
		t.Fatal("controller cannot observe a prevalidated accepted batch")
	}
	member := func(message, turn string, seq uint64) turncontinuation.Member {
		return turncontinuation.Member{TurnID: turn, Accepted: true, Input: telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: message, Sequence: seq}, Callbacks: telegramcontroller.DurableInputCallbacks{OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error {
			if !r.Accepted || r.Completion != telegramcontroller.DurableInputSucceeded {
				return errors.New("lost terminal proof")
			}
			committed <- r.MessageID
			return nil
		}}}
	}
	inputs := []turncontinuation.Member{member("next", "b", 9), member("steer", "a", 8), member("root", "a", 3)}
	if err := batch.ContinueAcceptedBatch(ctx, binding, inputs, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root", "next"} {
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("observed %q want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal("batch did not advance")
		}
	}
	current, _ := sessions.Load(ctx, running.ID())
	if current.Status() != domain.SessionRunning || finishes.Load() != 0 {
		t.Fatal("batch became Ready before all turns settled")
	}
	unsent, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: "queued-successor", Sequence: 10, Payload: []byte("must remain pending")}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
		return errors.New("successor must not be sent during historical observation")
	}})
	if unsent.Accepted || !errors.Is(err, turnprocessing.ErrInputDeferred) {
		t.Fatalf("queued successor lost definitely-unsent custody: accepted=%v err=%v", unsent.Accepted, err)
	}
	if err := batch.ContinueAcceptedBatch(ctx, binding, inputs, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	for _, want := range []string{"root", "steer", "next"} {
		select {
		case got := <-committed:
			if got != want {
				t.Fatalf("committed %q want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal("missing member completion")
		}
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.submits.Load() != 0 || finishes.Load() != 1 || len(observed) != 0 {
		t.Fatal("duplicate submit, observation, or lifecycle transition")
	}
}

func (s *acceptedObserver) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	s.submits.Add(1)
	return sessionruntime.TurnResult{}, errors.New("accepted input must never be submitted")
}
func (s *acceptedObserver) SubmitWithCallbacks(ctx context.Context, id domain.SessionID, text string, _ sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return s.Submit(ctx, id, text)
}
func (s *acceptedObserver) ObserveAcceptedWithCallbacks(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return s.observe(ctx, id, binding, cb)
}

type acceptedContinuation interface {
	ContinueAcceptedInput(context.Context, domain.ProviderBinding, telegramcontroller.DurableLeasedInput, telegramcontroller.DurableInputCallbacks) error
}

func TestAcceptedContinuationUsesExistingFinalPipelineWithoutSubmitting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 2)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := running.Binding()
	sessions := newLockedSessions(running)
	state := &retryFinalState{attempt: make(chan struct{}, 8)}
	observed := make(chan struct{}, 1)
	release := make(chan struct{})
	provider := &acceptedObserver{observe: func(ctx context.Context, id domain.SessionID, got domain.ProviderBinding, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if id != running.ID() || got != binding || cb.MessageID != "accepted-root" {
			return sessionruntime.TurnResult{}, errors.New("observation tuple changed")
		}
		observed <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return sessionruntime.TurnResult{}, ctx.Err()
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "exact recovered answer"}, nil
	}}
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 2)
	var accepted atomic.Int32
	c := newController(t, nil, sessions, submitterFunc(provider.Submit), nil, telegramcontroller.Options{UIState: state, AcceptedObserver: provider, TurnLifecycle: turnLifecycleFunc{
		start: func(context.Context, domain.SessionID) (domain.Session, error) {
			return domain.Session{}, errors.New("continuation must not start a new turn")
		},
		finish: func(_ context.Context, id domain.SessionID) (domain.Session, bool, error) {
			state.mu.Lock()
			saved := state.final
			state.mu.Unlock()
			if saved != "exact recovered answer" {
				return domain.Session{}, false, errors.New("Finish preceded final persistence")
			}
			finished, err := running.FinishWork(time.Now())
			if err == nil {
				sessions.Set(finished)
			}
			return finished, false, err
		},
	}})
	defer c.Close(context.Background())
	continuation, ok := any(c).(acceptedContinuation)
	if !ok {
		t.Fatal("controller cannot continue an already accepted turn without submitting")
	}
	input := telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: "accepted-root", Sequence: 9, Payload: []byte("must not be prepared or sent")}
	callbacks := telegramcontroller.DurableInputCallbacks{
		OnPrepared: func(context.Context, telegramcontroller.DurableInputPreparation) error {
			return errors.New("continuation must not preprocess")
		},
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
			accepted.Add(1)
			return errors.New("continuation must not reaccept")
		},
		OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
			completed <- receipt
			return nil
		},
	}
	if err := continuation.ContinueAcceptedInput(ctx, binding, input, callbacks); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("observer did not start")
	}
	if err := continuation.ContinueAcceptedInput(ctx, binding, input, callbacks); err != nil {
		t.Fatalf("exact duplicate continuation: %v", err)
	}
	close(release)
	select {
	case receipt := <-completed:
		if !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputSucceeded || receipt.SessionID != input.SessionID || receipt.MessageID != input.MessageID || receipt.Sequence != input.Sequence {
			t.Fatalf("completion lost exact tuple: %+v", receipt)
		}
	case <-ctx.Done():
		t.Fatal("accepted continuation did not complete")
	}
	if provider.submits.Load() != 0 || accepted.Load() != 0 {
		t.Fatal("continuation re-submitted or re-accepted the request")
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
		t.Fatal("duplicate continuation observed a second turn")
	default:
	}
	select {
	case <-completed:
		t.Fatal("duplicate completion")
	default:
	}
}
