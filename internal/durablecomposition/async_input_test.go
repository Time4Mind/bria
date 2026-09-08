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

type asyncProcessor func(context.Context, telegramcontroller.DurableLeasedInput, telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error)

func (p asyncProcessor) ProcessDurableInput(ctx context.Context, input telegramcontroller.DurableLeasedInput, callbacks telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
	return p(ctx, input, callbacks)
}

func TestPendingAcceptanceRetainsJournalUntilExactAsyncCompletion(t *testing.T) {
	for _, terminal := range []telegramcontroller.DurableInputCompletion{telegramcontroller.DurableInputSucceeded, telegramcontroller.DurableInputFailed, telegramcontroller.DurableInputUnknown} {
		t.Run(string(terminal), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(10, 0) }})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"first", "steering", "later"} {
				if _, err = flow.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
					t.Fatal(err)
				}
			}
			var complete func(context.Context, telegramcontroller.DurableInputProcessReceipt) error
			var receipt telegramcontroller.DurableInputProcessReceipt
			processor := durablecomposition.NewControllerInputProcessor(asyncProcessor(func(ctx context.Context, input telegramcontroller.DurableLeasedInput, callbacks telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
				if err := callbacks.OnAccepted(ctx, telegramcontroller.DurableInputAcceptance{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}); err != nil {
					return telegramcontroller.DurableInputProcessReceipt{}, err
				}
				complete = callbacks.OnCompleted
				receipt = telegramcontroller.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Accepted: true, Completion: telegramcontroller.DurableInputPending}
				return receipt, nil
			}))
			result, err := flow.ProcessNextInput(ctx, "s", processor)
			if err != nil || result.State != "accepted" {
				t.Fatalf("early acceptance = %#v, %v", result, err)
			}
			assertPhase := func(want messagejournal.InputPhase) {
				journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				inputs, err := journal.Inputs(ctx, "s")
				if err != nil || len(inputs) != 3 || inputs[0].Phase != want {
					t.Fatalf("persisted inputs = %#v, %v; want %s", inputs, err, want)
				}
			}
			assertPhase(messagejournal.InputAccepted)
			steering, err := journal.LeaseNextInput(ctx, "s", "steerer", time.Unix(20, 0), time.Minute)
			if err != nil || steering.MessageID != "steering" {
				t.Fatalf("accepted blocks steering: %#v %v", steering, err)
			}
			if _, err = journal.MarkInputAccepted(ctx, "s", "steering", "steerer"); err != nil {
				t.Fatal(err)
			}
			if complete == nil {
				t.Fatal("missing asynchronous completion callback")
			}
			receipt.Completion = terminal
			for _, corrupt := range []func(*telegramcontroller.DurableInputProcessReceipt){
				func(r *telegramcontroller.DurableInputProcessReceipt) { r.SessionID = domain.SessionID("other") },
				func(r *telegramcontroller.DurableInputProcessReceipt) { r.MessageID = "steering" },
				func(r *telegramcontroller.DurableInputProcessReceipt) { r.Sequence++ },
				func(r *telegramcontroller.DurableInputProcessReceipt) { r.Accepted = false },
			} {
				bad := receipt
				corrupt(&bad)
				if err := complete(ctx, bad); err == nil {
					t.Fatal("mismatched completion accepted")
				}
				assertPhase(messagejournal.InputAccepted)
			}
			// Recovery can seal unknown before the original continuation completes.
			if _, err = journal.MarkInputUnknown(ctx, "s", "first"); err != nil {
				t.Fatal(err)
			}
			if _, err = journal.LeaseNextInput(ctx, "s", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("unknown replay/unblock: %v", err)
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- complete(cancelled, receipt) }()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			want := messagejournal.InputUnknown
			if terminal == telegramcontroller.DurableInputSucceeded {
				want = messagejournal.InputCompleted
			}
			if terminal == telegramcontroller.DurableInputFailed {
				want = messagejournal.InputFailed
			}
			assertPhase(want)
			if terminal != telegramcontroller.DurableInputUnknown {
				conflict := receipt
				conflict.Completion = telegramcontroller.DurableInputUnknown
				if err = complete(ctx, conflict); err == nil {
					t.Fatal("terminal outcome overwritten")
				}
				assertPhase(want)
			}
		})
	}
}

type readySessions struct{ session domain.Session }

func (s readySessions) List(context.Context) ([]domain.Session, error) {
	return []domain.Session{s.session}, nil
}
func (s readySessions) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return s.session, nil
}

func TestDispatcherDrainsQueuedSteeringWithoutTerminalCompletion(t *testing.T) {
	dispatcherDrainsQueued(t, telegramcontroller.DurableInputPending, messagejournal.InputAccepted)
}

func TestDispatcherDrainsSynchronousTerminalFailuresWithoutReplay(t *testing.T) {
	dispatcherDrainsQueued(t, "terminal_failed", "terminal_failed")
}

func dispatcherDrainsQueued(t *testing.T, completion telegramcontroller.DurableInputCompletion, want messagejournal.InputPhase) {
	t.Helper()
	ctx := context.Background()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(10, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "steering-1", "steering-2"} {
		if _, err = flow.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
			t.Fatal(err)
		}
	}
	session, err := domain.NewStartingSessionAt("s", "intent", "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	processor := durablecomposition.NewControllerInputProcessor(asyncProcessor(func(ctx context.Context, input telegramcontroller.DurableLeasedInput, cb telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
		err := cb.OnAccepted(ctx, telegramcontroller.DurableInputAcceptance{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence})
		return telegramcontroller.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Accepted: true, Completion: completion}, err
	}))
	dispatcher := durablecomposition.InputDispatcher{Flow: flow, Processor: processor, Sessions: readySessions{session}}
	if err := dispatcher.ProcessReadySession(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	inputs, err := journal.Inputs(ctx, "s")
	if err != nil || len(inputs) != 3 {
		t.Fatalf("inputs: %#v %v", inputs, err)
	}
	for _, input := range inputs {
		if input.Phase != want {
			t.Fatalf("queued steering not drained: %#v", inputs)
		}
	}
}

func TestDispatcherLaterWakeSteersRunningButNotStoppingOrRecovery(t *testing.T) {
	for _, status := range []domain.SessionStatus{domain.SessionRunning, domain.SessionStopping, domain.SessionAwaitingRecovery} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(10, 0) }})
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
			processor := durablecomposition.NewControllerInputProcessor(asyncProcessor(func(ctx context.Context, input telegramcontroller.DurableLeasedInput, cb telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
				err := cb.OnAccepted(ctx, telegramcontroller.DurableInputAcceptance{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence})
				return telegramcontroller.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Accepted: true, Completion: telegramcontroller.DurableInputPending}, err
			}))
			dispatcher := durablecomposition.InputDispatcher{Flow: flow, Processor: processor, Sessions: readySessions{session}}
			if _, err := flow.EnqueueInput(ctx, "s", "first", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.ProcessReadySession(ctx, "s"); err != nil {
				t.Fatal(err)
			}
			// Model a later SessionStore snapshot after the initial drain:
			// the first turn is Running, not the initial Ready snapshot.
			session, err = session.StartWork(time.Unix(3, 0))
			if err != nil {
				t.Fatal(err)
			}
			if status == domain.SessionStopping {
				session, err = session.BeginStop(time.Unix(4, 0))
			}
			if status == domain.SessionAwaitingRecovery {
				session, err = session.AwaitRecoveryAt(time.Unix(4, 0))
			}
			if err != nil {
				t.Fatal(err)
			}
			dispatcher.Sessions = readySessions{session}
			if _, err := flow.EnqueueInput(ctx, "s", "later-steer", []byte("synthetic steer")); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.ProcessReadySession(ctx, "s"); err != nil {
				t.Fatal(err)
			}
			reopened, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := reopened.Inputs(ctx, "s")
			want := messagejournal.InputPending
			if status == domain.SessionRunning {
				want = messagejournal.InputAccepted
			}
			if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != want {
				t.Fatalf("later wake status=%s inputs=%#v read=%v; want first accepted and later %s", status, inputs, err, want)
			}
		})
	}
}
