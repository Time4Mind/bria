package durableflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/durableflow"
	"bria/internal/messagejournal"
)

type inputProviderFunc func(context.Context, durableflow.ProviderInput) (durableflow.HandoffResult, error)

func (fn inputProviderFunc) Handoff(ctx context.Context, input durableflow.ProviderInput) (durableflow.HandoffResult, error) {
	return fn(ctx, input)
}

type inputProcessorFunc func(context.Context, durableflow.ProviderInput, durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error)

func (fn inputProcessorFunc) Process(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
	return fn(ctx, input, callbacks)
}

type acceptedResolverFunc func(context.Context, durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error)

func (fn acceptedResolverFunc) ResolveAccepted(ctx context.Context, input durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
	return fn(ctx, input)
}

type outputSenderFunc func(context.Context, durableflow.ProviderOutput) (durableflow.DeliveryResult, error)

func (fn outputSenderFunc) Deliver(ctx context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
	return fn(ctx, output)
}

type confirmFailureJournal struct {
	*messagejournal.Journal
	err error
}

func (journal *confirmFailureJournal) ConfirmOutput(context.Context, string, string, string, string) (messagejournal.Output, error) {
	return messagejournal.Output{}, journal.err
}

func handoffReceipt(input durableflow.ProviderInput, state durableflow.HandoffState) durableflow.HandoffResult {
	return durableflow.HandoffResult{
		SessionID: input.SessionID,
		MessageID: input.MessageID,
		Sequence:  input.Sequence,
		State:     state,
	}
}

func deliveryReceipt(output durableflow.ProviderOutput, state durableflow.DeliveryState, receipt string) durableflow.DeliveryResult {
	return durableflow.DeliveryResult{
		SessionID:   output.SessionID,
		OperationID: output.OperationID,
		Sequence:    output.Sequence,
		State:       state,
		Receipt:     receipt,
	}
}

func openJournal(t *testing.T, path string) *messagejournal.Journal {
	t.Helper()
	limits := messagejournal.DefaultLimits()
	limits.MaxPendingInputsPerSession = 8
	journal, err := messagejournal.Open(path, limits)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return journal
}

func newFlow(t *testing.T, journal durableflow.Journal, provider durableflow.InputProvider, sender durableflow.OutputSender, now time.Time) *durableflow.Flow {
	t.Helper()
	flow, err := durableflow.New(journal, provider, sender, durableflow.Options{
		Owner:         "worker-a",
		LeaseDuration: time.Minute,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return flow
}

func TestEnqueueAcknowledgesOnlyDurableStableInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))

	receipt, err := flow.EnqueueInput(context.Background(), "session-a", "telegram:42", []byte("hello"))
	if err != nil {
		t.Fatalf("EnqueueInput() error = %v", err)
	}
	if !receipt.Inserted || receipt.MessageID != "telegram:42" || receipt.Sequence != 1 {
		t.Fatalf("receipt = %#v", receipt)
	}

	reopened := openJournal(t, path)
	inputs, err := reopened.Inputs(context.Background(), "session-a")
	if err != nil || len(inputs) != 1 || string(inputs[0].Payload) != "hello" {
		t.Fatalf("persisted inputs = %#v, %v", inputs, err)
	}
	replayed := newFlow(t, reopened, nil, nil, time.Unix(11, 0))
	receipt, err = replayed.EnqueueInput(context.Background(), "session-a", "telegram:42", []byte("hello"))
	if err != nil || receipt.Inserted || receipt.Sequence != 1 {
		t.Fatalf("idempotent EnqueueInput() = %#v, %v", receipt, err)
	}
}

func TestPublicLeasedAcceptanceAndCompletionRequireExactTuple(t *testing.T) {
	journal := openJournal(t, filepath.Join(t.TempDir(), "journal.json"))
	flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
	receipt, err := flow.EnqueueInput(context.Background(), "session-a", "message-a", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LeaseNextInput(context.Background(), "session-a", "worker-a", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.RecordLeasedInputAccepted(context.Background(), "session-a", "message-a", receipt.Sequence+1); !errors.Is(err, durableflow.ErrInvalidHandoff) {
		t.Fatalf("mismatched acceptance error = %v", err)
	}
	accepted, err := flow.RecordLeasedInputAccepted(context.Background(), "session-a", "message-a", receipt.Sequence)
	if err != nil || accepted.State != durableflow.HandoffAccepted || accepted.Sequence != receipt.Sequence {
		t.Fatalf("exact acceptance = (%#v, %v)", accepted, err)
	}
	if replayed, err := flow.RecordLeasedInputAccepted(context.Background(), "session-a", "message-a", receipt.Sequence); err != nil || replayed.State != durableflow.HandoffAccepted {
		t.Fatalf("idempotent exact acceptance = (%#v, %v)", replayed, err)
	}
	if err := flow.RecordInputCompletionExact(context.Background(), "session-a", "message-a", receipt.Sequence+1, durableflow.CompletionSucceeded); !errors.Is(err, durableflow.ErrInvalidHandoff) {
		t.Fatalf("mismatched completion error = %v", err)
	}
	if err := flow.RecordInputCompletionExact(context.Background(), "session-a", "message-a", receipt.Sequence, durableflow.CompletionSucceeded); err != nil {
		t.Fatalf("exact completion error = %v", err)
	}
	if err := flow.RecordInputCompletionExact(context.Background(), "session-a", "message-a", receipt.Sequence, durableflow.CompletionSucceeded); err != nil {
		t.Fatalf("idempotent exact completion error = %v", err)
	}
	inputs, err := journal.Inputs(context.Background(), "session-a")
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputCompleted {
		t.Fatalf("completed inputs = (%#v, %v)", inputs, err)
	}
}

func TestProcessNextInputPersistsAcceptanceInsideCallbackBeforeTerminal(t *testing.T) {
	journal := openJournal(t, filepath.Join(t.TempDir(), "journal.json"))
	flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
	if _, err := flow.EnqueueInput(context.Background(), "session-a", "message-a", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	processor := inputProcessorFunc(func(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		acceptance := handoffReceipt(input, durableflow.HandoffAccepted)
		if err := callbacks.OnAccepted(ctx, acceptance); err != nil {
			return durableflow.InputProcessResult{}, err
		}
		inputs, err := journal.Inputs(ctx, input.SessionID)
		if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputAccepted {
			t.Fatalf("state inside provider continuation = (%#v, %v)", inputs, err)
		}
		return durableflow.InputProcessResult{
			SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessCompleted,
		}, nil
	})
	result, err := flow.ProcessNextInput(context.Background(), "session-a", processor)
	if err != nil || result.State != durableflow.InputProcessCompleted {
		t.Fatalf("ProcessNextInput() = (%#v, %v)", result, err)
	}
	inputs, err := journal.Inputs(context.Background(), "session-a")
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputCompleted {
		t.Fatalf("terminal state = (%#v, %v)", inputs, err)
	}
}

func TestProcessNextInputSealsCrashAfterAcceptanceAsUnknown(t *testing.T) {
	journal := openJournal(t, filepath.Join(t.TempDir(), "journal.json"))
	flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
	if _, err := flow.EnqueueInput(context.Background(), "session-a", "message-a", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	processor := inputProcessorFunc(func(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		if err := callbacks.OnAccepted(ctx, handoffReceipt(input, durableflow.HandoffAccepted)); err != nil {
			return durableflow.InputProcessResult{}, err
		}
		return durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}, errors.New("provider connection lost")
	})
	result, err := flow.ProcessNextInput(context.Background(), "session-a", processor)
	if err == nil || result.State != durableflow.InputProcessUnknown {
		t.Fatalf("ProcessNextInput() = (%#v, %v), want unknown", result, err)
	}
	inputs, readErr := journal.Inputs(context.Background(), "session-a")
	if readErr != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputUnknown {
		t.Fatalf("crash state = (%#v, %v)", inputs, readErr)
	}
}

func TestDispatchRecordsRealAcceptanceAndIndependentCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	var mu sync.Mutex
	var got []durableflow.ProviderInput
	provider := inputProviderFunc(func(_ context.Context, input durableflow.ProviderInput) (durableflow.HandoffResult, error) {
		mu.Lock()
		got = append(got, input)
		mu.Unlock()
		return handoffReceipt(input, durableflow.HandoffAccepted), nil
	})
	flow := newFlow(t, journal, provider, nil, time.Unix(20, 0))
	for _, item := range []struct{ id, body string }{{"m1", "first"}, {"m2", "second"}} {
		if _, err := flow.EnqueueInput(context.Background(), "session-a", item.id, []byte(item.body)); err != nil {
			t.Fatal(err)
		}
	}

	first, err := flow.DispatchNextInput(context.Background(), "session-a")
	if err != nil || first.State != durableflow.HandoffAccepted || first.MessageID != "m1" {
		t.Fatalf("first dispatch = %#v, %v", first, err)
	}
	second, err := flow.DispatchNextInput(context.Background(), "session-a")
	if err != nil || second.State != durableflow.HandoffAccepted || second.MessageID != "m2" {
		t.Fatalf("second dispatch = %#v, %v", second, err)
	}
	mu.Lock()
	if len(got) != 2 || got[0].Sequence != 1 || got[1].Sequence != 2 || got[0].MessageID != "m1" || got[1].MessageID != "m2" {
		t.Fatalf("provider inputs = %#v", got)
	}
	mu.Unlock()

	if err := flow.RecordInputCompletion(context.Background(), "session-a", "m1", durableflow.CompletionSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := flow.RecordInputCompletion(context.Background(), "session-a", "m2", durableflow.CompletionFailed); err != nil {
		t.Fatal(err)
	}
	inputs, err := openJournal(t, path).Inputs(context.Background(), "session-a")
	if err != nil || inputs[0].Phase != messagejournal.InputCompleted || inputs[1].Phase != messagejournal.InputFailed {
		t.Fatalf("completion phases = %#v, %v", inputs, err)
	}
}

func TestInputHandoffOutcomesAreFailSafeAndNeverImplicitlyRetried(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outcome     durableflow.HandoffState
		providerErr error
		wantPhase   messagejournal.InputPhase
	}{
		{name: "deferred", outcome: durableflow.HandoffDeferred, wantPhase: messagejournal.InputPending},
		{name: "rejected", outcome: durableflow.HandoffRejected, wantPhase: messagejournal.InputFailed},
		{name: "unknown", outcome: durableflow.HandoffUnknown, providerErr: errors.New("connection lost"), wantPhase: messagejournal.InputUnknown},
		{name: "unclassified error", providerErr: errors.New("broken adapter"), wantPhase: messagejournal.InputUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.json")
			journal := openJournal(t, path)
			provider := inputProviderFunc(func(_ context.Context, input durableflow.ProviderInput) (durableflow.HandoffResult, error) {
				return handoffReceipt(input, tc.outcome), tc.providerErr
			})
			flow := newFlow(t, journal, provider, nil, time.Unix(30, 0))
			if _, err := flow.EnqueueInput(context.Background(), "session-a", "m1", []byte("body")); err != nil {
				t.Fatal(err)
			}
			result, dispatchErr := flow.DispatchNextInput(context.Background(), "session-a")
			if tc.providerErr != nil && dispatchErr == nil {
				t.Fatal("DispatchNextInput() error = nil")
			}
			if result.State != tc.outcome && !(tc.outcome == "" && result.State == durableflow.HandoffUnknown) {
				t.Fatalf("dispatch state = %q", result.State)
			}
			inputs, err := openJournal(t, path).Inputs(context.Background(), "session-a")
			if err != nil || len(inputs) != 1 || inputs[0].Phase != tc.wantPhase {
				t.Fatalf("persisted input = %#v, %v", inputs, err)
			}
			if tc.wantPhase == messagejournal.InputFailed || tc.wantPhase == messagejournal.InputUnknown {
				if _, err := openJournal(t, path).LeaseNextInput(context.Background(), "session-a", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
					t.Fatalf("failed input was automatically retryable: %v", err)
				}
			}
		})
	}
}

func TestCanceledHandoffStillSealsAmbiguousInputBeforeReturning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	provider := inputProviderFunc(func(context.Context, durableflow.ProviderInput) (durableflow.HandoffResult, error) {
		cancel()
		return durableflow.HandoffResult{}, context.Canceled
	})
	flow := newFlow(t, journal, provider, nil, time.Unix(40, 0))
	if _, err := flow.EnqueueInput(context.Background(), "session-a", "m1", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.DispatchNextInput(ctx, "session-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DispatchNextInput() error = %v, want context canceled", err)
	}
	inputs, err := openJournal(t, path).Inputs(context.Background(), "session-a")
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputUnknown {
		t.Fatalf("persisted input after cancellation = %#v, %v", inputs, err)
	}
}

func TestMismatchedAcceptedReceiptSealsInputAsAmbiguous(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	provider := inputProviderFunc(func(_ context.Context, input durableflow.ProviderInput) (durableflow.HandoffResult, error) {
		return durableflow.HandoffResult{
			SessionID: input.SessionID,
			MessageID: "different-message",
			Sequence:  input.Sequence,
			State:     durableflow.HandoffAccepted,
		}, nil
	})
	flow := newFlow(t, journal, provider, nil, time.Unix(45, 0))
	if _, err := flow.EnqueueInput(context.Background(), "session-a", "m1", []byte("body")); err != nil {
		t.Fatal(err)
	}
	result, err := flow.DispatchNextInput(context.Background(), "session-a")
	if !errors.Is(err, durableflow.ErrInvalidHandoff) || result.State != durableflow.HandoffUnknown {
		t.Fatalf("mismatched handoff = %#v, %v", result, err)
	}
	inputs, readErr := openJournal(t, path).Inputs(context.Background(), "session-a")
	if readErr != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputUnknown {
		t.Fatalf("persisted mismatched handoff = %#v, %v", inputs, readErr)
	}
	if _, leaseErr := openJournal(t, path).LeaseNextInput(context.Background(), "session-a", "other", time.Unix(999, 0), time.Minute); !errors.Is(leaseErr, messagejournal.ErrNoAvailable) {
		t.Fatalf("mismatched handoff was automatically retryable: %v", leaseErr)
	}
}

func TestExpiredInputLeaseIsRecoveredAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	if _, _, err := journal.EnqueueInput(context.Background(), "session-a", "m1", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LeaseNextInput(context.Background(), "session-a", "crashed-worker", time.Unix(1, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	provider := inputProviderFunc(func(_ context.Context, input durableflow.ProviderInput) (durableflow.HandoffResult, error) {
		return handoffReceipt(input, durableflow.HandoffAccepted), nil
	})
	flow := newFlow(t, openJournal(t, path), provider, nil, time.Unix(62, 0))
	result, err := flow.DispatchNextInput(context.Background(), "session-a")
	if err != nil || result.State != durableflow.HandoffAccepted || result.MessageID != "m1" {
		t.Fatalf("recovered dispatch = %#v, %v", result, err)
	}
}

func TestFailedInputRequiresExplicitRetryAndKeepsOrderAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	states := []durableflow.HandoffState{durableflow.HandoffRejected, durableflow.HandoffAccepted, durableflow.HandoffAccepted}
	var got []string
	provider := inputProviderFunc(func(_ context.Context, input durableflow.ProviderInput) (durableflow.HandoffResult, error) {
		got = append(got, input.MessageID)
		state := states[0]
		states = states[1:]
		return handoffReceipt(input, state), nil
	})
	flow := newFlow(t, journal, provider, nil, time.Unix(65, 0))
	for _, id := range []string{"m1", "m2"} {
		if _, err := flow.EnqueueInput(context.Background(), "session-a", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := flow.DispatchNextInput(context.Background(), "session-a"); err != nil || result.State != durableflow.HandoffRejected {
		t.Fatalf("rejected dispatch = %#v, %v", result, err)
	}

	reopened := newFlow(t, openJournal(t, path), provider, nil, time.Unix(66, 0))
	if _, err := reopened.DispatchNextInput(context.Background(), "session-a"); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("dispatch before explicit retry = %v, want ErrNoAvailable", err)
	}
	if err := reopened.RetryInput(context.Background(), "session-a", "m1"); err != nil {
		t.Fatalf("RetryInput() error = %v", err)
	}
	first, err := reopened.DispatchNextInput(context.Background(), "session-a")
	if err != nil || first.State != durableflow.HandoffAccepted || first.MessageID != "m1" || first.Sequence != 1 {
		t.Fatalf("retried first dispatch = %#v, %v", first, err)
	}
	second, err := reopened.DispatchNextInput(context.Background(), "session-a")
	if err != nil || second.State != durableflow.HandoffAccepted || second.MessageID != "m2" || second.Sequence != 2 {
		t.Fatalf("ordered second dispatch = %#v, %v", second, err)
	}
	if want := []string{"m1", "m1", "m2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("provider order = %#v, want %#v", got, want)
	}
}

func TestReconcileAcceptedInputsPersistsProviderHistoryOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	for index, id := range []string{"m1", "m2", "m3"} {
		if _, _, err := journal.EnqueueInput(context.Background(), "session-a", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
		leased, err := journal.LeaseNextInput(context.Background(), "session-a", "worker-a", time.Unix(int64(200+index), 0), time.Minute)
		if err != nil || leased.MessageID != id {
			t.Fatalf("lease %s = %#v, %v", id, leased, err)
		}
		if _, err := journal.MarkInputAccepted(context.Background(), "session-a", id, "worker-a"); err != nil {
			t.Fatalf("accept %s: %v", id, err)
		}
	}
	want := map[string]durableflow.AcceptedResolution{
		"m1": durableflow.AcceptedCompleted,
		"m2": durableflow.AcceptedFailed,
		"m3": durableflow.AcceptedUnknown,
	}
	resolver := acceptedResolverFunc(func(_ context.Context, input durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
		return durableflow.AcceptedResolutionResult{
			SessionID:  input.SessionID,
			MessageID:  input.MessageID,
			Sequence:   input.Sequence,
			Resolution: want[input.MessageID],
		}, nil
	})
	flow := newFlow(t, openJournal(t, path), nil, nil, time.Unix(210, 0))
	results, err := flow.ReconcileAcceptedInputs(context.Background(), "session-a", resolver)
	if err != nil || len(results) != 3 {
		t.Fatalf("ReconcileAcceptedInputs() = %#v, %v", results, err)
	}
	inputs, err := openJournal(t, path).Inputs(context.Background(), "session-a")
	if err != nil || len(inputs) != 3 || inputs[0].Phase != messagejournal.InputCompleted || inputs[1].Phase != messagejournal.InputFailed || inputs[2].Phase != messagejournal.InputUnknown {
		t.Fatalf("reconciled durable phases = %#v, %v", inputs, err)
	}
	if _, err := openJournal(t, path).LeaseNextInput(context.Background(), "session-a", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("unknown reconciled input was automatically replayable: %v", err)
	}
}

func TestReconcileAcceptedInputRejectsMismatchedHistoryReceiptAsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	if _, _, err := journal.EnqueueInput(context.Background(), "session-a", "m1", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LeaseNextInput(context.Background(), "session-a", "worker-a", time.Unix(220, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInputAccepted(context.Background(), "session-a", "m1", "worker-a"); err != nil {
		t.Fatal(err)
	}
	resolver := acceptedResolverFunc(func(_ context.Context, input durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
		return durableflow.AcceptedResolutionResult{
			SessionID:  input.SessionID,
			MessageID:  "different-message",
			Sequence:   input.Sequence,
			Resolution: durableflow.AcceptedCompleted,
		}, nil
	})
	flow := newFlow(t, journal, nil, nil, time.Unix(221, 0))
	results, err := flow.ReconcileAcceptedInputs(context.Background(), "session-a", resolver)
	if !errors.Is(err, durableflow.ErrInvalidResolution) || len(results) != 1 || results[0].Resolution != durableflow.AcceptedUnknown {
		t.Fatalf("mismatched reconciliation = %#v, %v", results, err)
	}
	inputs, readErr := openJournal(t, path).Inputs(context.Background(), "session-a")
	if readErr != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputUnknown {
		t.Fatalf("mismatched reconciliation phase = %#v, %v", inputs, readErr)
	}
}

func TestConcurrentSessionsDoNotShareALane(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	provider := inputProviderFunc(func(ctx context.Context, input durableflow.ProviderInput) (durableflow.HandoffResult, error) {
		if input.SessionID == "session-a" {
			close(enteredA)
			select {
			case <-releaseA:
			case <-ctx.Done():
				return durableflow.HandoffResult{}, ctx.Err()
			}
		}
		return handoffReceipt(input, durableflow.HandoffAccepted), nil
	})
	flow := newFlow(t, journal, provider, nil, time.Unix(70, 0))
	for _, sessionID := range []string{"session-a", "session-b"} {
		if _, err := flow.EnqueueInput(context.Background(), sessionID, "m1", []byte(sessionID)); err != nil {
			t.Fatal(err)
		}
	}
	aDone := make(chan error, 1)
	go func() { _, err := flow.DispatchNextInput(context.Background(), "session-a"); aDone <- err }()
	<-enteredA
	bDone := make(chan error, 1)
	go func() { _, err := flow.DispatchNextInput(context.Background(), "session-b"); bDone <- err }()
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("session-b dispatch error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session-b was blocked by session-a")
	}
	close(releaseA)
	if err := <-aDone; err != nil {
		t.Fatalf("session-a dispatch error = %v", err)
	}
}

func TestOutputDeliveryPersistsConfirmedFailedAndUnknownWithoutAutoRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	states := []durableflow.DeliveryResult{
		{State: durableflow.DeliveryConfirmed, Receipt: "telegram:100"},
		{State: durableflow.DeliveryUnknown},
	}
	sender := outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		result := states[0]
		states = states[1:]
		return deliveryReceipt(output, result.State, result.Receipt), nil
	})
	flow := newFlow(t, journal, nil, sender, time.Unix(80, 0))
	for _, item := range []struct{ id, kind, body string }{{"o1", "final", "one"}, {"o2", "file", "two"}} {
		receipt, err := flow.EnqueueOutput(context.Background(), "session-a", item.id, item.kind, []byte(item.body))
		if err != nil || !receipt.Inserted {
			t.Fatalf("EnqueueOutput() = %#v, %v", receipt, err)
		}
	}
	first, err := flow.DeliverNextOutput(context.Background(), "session-a")
	if err != nil || first.State != durableflow.DeliveryConfirmed || first.OperationID != "o1" {
		t.Fatalf("first output = %#v, %v", first, err)
	}
	second, err := flow.DeliverNextOutput(context.Background(), "session-a")
	if err != nil || second.State != durableflow.DeliveryUnknown || second.OperationID != "o2" {
		t.Fatalf("second output = %#v, %v", second, err)
	}

	reopened := openJournal(t, path)
	outputs, err := reopened.Outputs(context.Background(), "session-a")
	if err != nil || outputs[0].Phase != messagejournal.OutputConfirmed || outputs[0].Receipt != "telegram:100" || outputs[1].Phase != messagejournal.OutputUnknown {
		t.Fatalf("persisted outputs = %#v, %v", outputs, err)
	}
	if _, err := reopened.LeaseNextOutput(context.Background(), "session-a", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("unknown output was automatically retried: %v", err)
	}
}

func TestInvalidOrErroredDeliveryIsPersistedUnknown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result durableflow.DeliveryResult
		err    error
	}{
		{name: "missing receipt", result: durableflow.DeliveryResult{State: durableflow.DeliveryConfirmed}},
		{name: "failed with receipt", result: durableflow.DeliveryResult{State: durableflow.DeliveryFailed, Receipt: "impossible-receipt"}},
		{name: "unknown with receipt", result: durableflow.DeliveryResult{State: durableflow.DeliveryUnknown, Receipt: "untrusted-receipt"}},
		{name: "transport error", err: errors.New("timeout")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.json")
			journal := openJournal(t, path)
			sender := outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
				return deliveryReceipt(output, tc.result.State, tc.result.Receipt), tc.err
			})
			flow := newFlow(t, journal, nil, sender, time.Unix(90, 0))
			if _, err := flow.EnqueueOutput(context.Background(), "session-a", "o1", "final", []byte("body")); err != nil {
				t.Fatal(err)
			}
			result, err := flow.DeliverNextOutput(context.Background(), "session-a")
			if err == nil || result.State != durableflow.DeliveryUnknown {
				t.Fatalf("DeliverNextOutput() = %#v, %v", result, err)
			}
			outputs, readErr := openJournal(t, path).Outputs(context.Background(), "session-a")
			if readErr != nil || outputs[0].Phase != messagejournal.OutputUnknown {
				t.Fatalf("persisted output = %#v, %v", outputs, readErr)
			}
		})
	}
}

func TestCanceledDeliveryStillSealsUnknownOutputBeforeReturning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	sender := outputSenderFunc(func(context.Context, durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		cancel()
		return durableflow.DeliveryResult{}, context.Canceled
	})
	flow := newFlow(t, journal, nil, sender, time.Unix(100, 0))
	if _, err := flow.EnqueueOutput(context.Background(), "session-a", "o1", "final", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.DeliverNextOutput(ctx, "session-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeliverNextOutput() error = %v, want context canceled", err)
	}
	outputs, err := openJournal(t, path).Outputs(context.Background(), "session-a")
	if err != nil || len(outputs) != 1 || outputs[0].Phase != messagejournal.OutputUnknown {
		t.Fatalf("persisted output after cancellation = %#v, %v", outputs, err)
	}
}

func TestMismatchedConfirmedReceiptSealsOutputAsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	sender := outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		return durableflow.DeliveryResult{
			SessionID:   output.SessionID,
			OperationID: "different-operation",
			Sequence:    output.Sequence,
			State:       durableflow.DeliveryConfirmed,
			Receipt:     "telegram:100",
		}, nil
	})
	flow := newFlow(t, journal, nil, sender, time.Unix(105, 0))
	if _, err := flow.EnqueueOutput(context.Background(), "session-a", "o1", "final", []byte("body")); err != nil {
		t.Fatal(err)
	}
	result, err := flow.DeliverNextOutput(context.Background(), "session-a")
	if !errors.Is(err, durableflow.ErrInvalidDelivery) || result.State != durableflow.DeliveryUnknown {
		t.Fatalf("mismatched delivery = %#v, %v", result, err)
	}
	outputs, readErr := openJournal(t, path).Outputs(context.Background(), "session-a")
	if readErr != nil || len(outputs) != 1 || outputs[0].Phase != messagejournal.OutputUnknown || outputs[0].Receipt != "" {
		t.Fatalf("persisted mismatched delivery = %#v, %v", outputs, readErr)
	}
	if _, leaseErr := openJournal(t, path).LeaseNextOutput(context.Background(), "session-a", "other", time.Unix(999, 0), time.Minute); !errors.Is(leaseErr, messagejournal.ErrNoAvailable) {
		t.Fatalf("mismatched delivery was automatically retryable: %v", leaseErr)
	}
}

func TestConfirmedTransportWithPersistenceFailureIsSealedUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	persistErr := errors.New("confirm persistence failed")
	failing := &confirmFailureJournal{Journal: journal, err: persistErr}
	sender := outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		return deliveryReceipt(output, durableflow.DeliveryConfirmed, "telegram:100"), nil
	})
	flow := newFlow(t, failing, nil, sender, time.Unix(107, 0))
	if _, err := flow.EnqueueOutput(context.Background(), "session-a", "o1", "final", []byte("body")); err != nil {
		t.Fatal(err)
	}
	result, err := flow.DeliverNextOutput(context.Background(), "session-a")
	if !errors.Is(err, persistErr) || result.State != durableflow.DeliveryUnknown || result.Receipt != "" {
		t.Fatalf("delivery with confirmation persistence failure = %#v, %v", result, err)
	}
	outputs, readErr := openJournal(t, path).Outputs(context.Background(), "session-a")
	if readErr != nil || len(outputs) != 1 || outputs[0].Phase != messagejournal.OutputUnknown {
		t.Fatalf("fail-safe output state = %#v, %v", outputs, readErr)
	}
	if _, leaseErr := openJournal(t, path).LeaseNextOutput(context.Background(), "session-a", "other", time.Unix(999, 0), time.Minute); !errors.Is(leaseErr, messagejournal.ErrNoAvailable) {
		t.Fatalf("confirmation persistence failure was automatically retryable: %v", leaseErr)
	}
}

func TestUnknownOutputRequiresExplicitRetryAndKeepsOrderAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	states := []durableflow.DeliveryResult{
		{State: durableflow.DeliveryUnknown},
		{State: durableflow.DeliveryConfirmed, Receipt: "telegram:1"},
		{State: durableflow.DeliveryConfirmed, Receipt: "telegram:2"},
	}
	var got []string
	sender := outputSenderFunc(func(_ context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
		got = append(got, output.OperationID)
		result := states[0]
		states = states[1:]
		return deliveryReceipt(output, result.State, result.Receipt), nil
	})
	flow := newFlow(t, journal, nil, sender, time.Unix(110, 0))
	for _, id := range []string{"o1", "o2"} {
		if _, err := flow.EnqueueOutput(context.Background(), "session-a", id, "final", []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := flow.DeliverNextOutput(context.Background(), "session-a"); err != nil || result.State != durableflow.DeliveryUnknown {
		t.Fatalf("unknown delivery = %#v, %v", result, err)
	}

	reopened := newFlow(t, openJournal(t, path), nil, sender, time.Unix(111, 0))
	if _, err := reopened.DeliverNextOutput(context.Background(), "session-a"); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("delivery before explicit retry = %v, want ErrNoAvailable", err)
	}
	if err := reopened.RetryOutput(context.Background(), "session-a", "o1"); err != nil {
		t.Fatalf("RetryOutput() error = %v", err)
	}
	first, err := reopened.DeliverNextOutput(context.Background(), "session-a")
	if err != nil || first.State != durableflow.DeliveryConfirmed || first.OperationID != "o1" || first.Sequence != 1 {
		t.Fatalf("retried first delivery = %#v, %v", first, err)
	}
	second, err := reopened.DeliverNextOutput(context.Background(), "session-a")
	if err != nil || second.State != durableflow.DeliveryConfirmed || second.OperationID != "o2" || second.Sequence != 2 {
		t.Fatalf("ordered second delivery = %#v, %v", second, err)
	}
	if want := []string{"o1", "o1", "o2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("sender order = %#v, want %#v", got, want)
	}
	outputs, err := openJournal(t, path).Outputs(context.Background(), "session-a")
	if err != nil || outputs[0].Receipt != "telegram:1" || outputs[1].Receipt != "telegram:2" {
		t.Fatalf("confirmed outputs = %#v, %v", outputs, err)
	}
}
