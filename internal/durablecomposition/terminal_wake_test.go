package durablecomposition_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
)

type wakeSessions struct {
	mu      sync.Mutex
	session domain.Session
	loaded  chan domain.SessionStatus
}

func (s *wakeSessions) List(context.Context) ([]domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []domain.Session{s.session}, nil
}
func (s *wakeSessions) Load(context.Context, domain.SessionID) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.loaded <- s.session.Status():
	default:
	}
	return s.session, nil
}
func (s *wakeSessions) set(session domain.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = session
}

func TestTerminalWakeDrainsStoppingQueueWithoutNewIncomingWake(t *testing.T) {
	for _, outcome := range []telegramcontroller.DurableInputCompletion{telegramcontroller.DurableInputSucceeded, "terminal_failed", telegramcontroller.DurableInputFailed, telegramcontroller.DurableInputUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			session, err := domain.NewStartingSessionAt("s", "intent", "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
			if err != nil {
				t.Fatal(err)
			}
			session, err = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}, time.Unix(2, 0))
			if err != nil {
				t.Fatal(err)
			}
			store := &wakeSessions{session: session, loaded: make(chan domain.SessionStatus, 16)}
			custody := durablecomposition.InputCustody{Flow: flow, Wake: make(chan domain.SessionID, 8)}
			type continuation struct {
				receipt  telegramcontroller.DurableInputProcessReceipt
				complete func(context.Context, telegramcontroller.DurableInputProcessReceipt) error
			}
			accepted := make(chan continuation, 1)
			processed := make(chan string, 8)
			processor := durablecomposition.NewControllerInputProcessor(asyncProcessor(func(ctx context.Context, input telegramcontroller.DurableLeasedInput, cb telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
				if err := cb.OnAccepted(ctx, telegramcontroller.DurableInputAcceptance{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}); err != nil {
					return telegramcontroller.DurableInputProcessReceipt{}, err
				}
				receipt := telegramcontroller.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Accepted: true, Completion: telegramcontroller.DurableInputSucceeded}
				if input.MessageID == "parent" {
					receipt.Completion = telegramcontroller.DurableInputPending
					accepted <- continuation{receipt, cb.OnCompleted}
				} else {
					processed <- input.MessageID
				}
				return receipt, nil
			}), custody.WakeSession)
			if _, err := flow.EnqueueInput(ctx, "s", "parent", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			dispatcher := durablecomposition.InputDispatcher{Flow: flow, Processor: processor, Sessions: store, Wake: custody.Wake, Report: func(err error) { t.Errorf("dispatch: %v", err) }}
			done := make(chan error, 1)
			go func() { done <- dispatcher.Run(ctx) }()
			stopped := false
			defer func() {
				if stopped {
					return
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("dispatcher shutdown: %v", err)
				}
			}()
			var parent continuation
			select {
			case parent = <-accepted:
			case <-ctx.Done():
				t.Fatal("parent not accepted")
			}
			session, err = session.StartWork(time.Unix(3, 0))
			if err != nil {
				t.Fatal(err)
			}
			session, err = session.BeginStop(time.Unix(4, 0))
			if err != nil {
				t.Fatal(err)
			}
			store.set(session)
			// Ensure the initial ready drain has ended before enqueueing a later
			// stopping input. This wake's Load is the next dispatcher invocation.
			custody.WakeSession("s")
			waitStopping := func() {
				t.Helper()
				for {
					select {
					case status := <-store.loaded:
						if status == domain.SessionStopping {
							return
						}
					case <-ctx.Done():
						t.Fatal("stopping wake not consumed")
					}
				}
			}
			waitStopping()
			if _, err := custody.Accept(ctx, telegramcontroller.SessionInput{SessionID: "s", MessageID: "after-stop", Payload: []byte("synthetic later")}); err != nil {
				t.Fatal(err)
			}
			waitStopping()
			session, err = session.FinishWork(time.Unix(5, 0))
			if err != nil {
				t.Fatal(err)
			}
			store.set(session)
			parent.receipt.Completion = outcome
			for i := 0; i < 3; i++ {
				if err := parent.complete(ctx, parent.receipt); err != nil {
					t.Fatal(err)
				}
			}
			wantParent, wantLater := messagejournal.InputCompleted, messagejournal.InputCompleted
			if outcome == telegramcontroller.DurableInputSucceeded || outcome == "terminal_failed" {
				if outcome == "terminal_failed" {
					wantParent = "terminal_failed"
				}
				select {
				case id := <-processed:
					if id != "after-stop" {
						t.Fatalf("replayed input %q", id)
					}
				case <-time.After(time.Second):
					t.Fatal("committed terminal did not wake queued input")
				}
			} else {
				wantLater = messagejournal.InputPending
				wantParent = messagejournal.InputPhase(outcome)
				if err := dispatcher.ProcessReadySession(ctx, "s"); err != nil {
					t.Fatal(err)
				}
			}
			// Stop after the processor returns so the physical completion is
			// committed before reopening; this is not an extra incoming wake.
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			stopped = true
			reopened, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := reopened.Inputs(context.Background(), "s")
			if err != nil || len(inputs) != 2 || inputs[0].Phase != wantParent || inputs[1].Phase != wantLater {
				t.Fatalf("terminal wake phases: %#v %v", inputs, err)
			}
			select {
			case id := <-processed:
				t.Fatalf("duplicate/unblocked provider input %q", id)
			default:
			}
		})
	}
}

func TestWakeSessionNeverBlocksOnNilOrSaturatedQueue(t *testing.T) {
	for _, wake := range []chan domain.SessionID{nil, make(chan domain.SessionID, 1)} {
		custody := durablecomposition.InputCustody{Wake: wake}
		if wake != nil {
			wake <- "existing"
		}
		done := make(chan struct{})
		go func() { custody.WakeSession("s"); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("completion wake blocked")
		}
	}
}
