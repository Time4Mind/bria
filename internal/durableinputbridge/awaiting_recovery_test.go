package durableinputbridge_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/durableinputbridge"
	"bria/internal/turnprocessing"
)

func TestAwaitingRecoveryPreservesAcceptanceThroughVerifiedCallbackWithoutWake(t *testing.T) {
	ctx := context.Background()
	input := durableflow.ProviderInput{SessionID: "session", MessageID: "message", Sequence: 7}
	receipt := turnprocessing.DurableInputProcessReceipt{SessionID: "session", MessageID: "message", Sequence: 7, Accepted: true, Completion: "awaiting_recovery"}
	var complete func(context.Context, turnprocessing.DurableInputProcessReceipt) error
	var verified []durableflow.InputProcessResult
	fault := errors.New("synthetic custody write failure")
	var callbackErr error
	wakes := 0
	processor := durableinputbridge.New(processorFunc(func(ctx context.Context, leased turnprocessing.DurableLeasedInput, cb turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error) {
		if err := cb.OnAccepted(ctx, turnprocessing.DurableInputAcceptance{SessionID: leased.SessionID, MessageID: leased.MessageID, Sequence: leased.Sequence}); err != nil {
			return turnprocessing.DurableInputProcessReceipt{}, err
		}
		complete = cb.OnCompleted
		return receipt, nil
	}), func(domain.SessionID) { wakes++ })
	result, err := processor.Process(ctx, input, durableflow.InputProcessCallbacks{
		OnAccepted: func(context.Context, durableflow.HandoffResult) error { return nil },
		OnCompleted: func(_ context.Context, result durableflow.InputProcessResult) error {
			verified = append(verified, result)
			return callbackErr
		},
	})
	if err != nil || result.State != durableflow.InputProcessAccepted {
		t.Fatalf("proven acceptance was lost: %+v, %v", result, err)
	}
	callbackErr = fault
	if err := complete(ctx, receipt); !errors.Is(err, fault) {
		t.Fatalf("pending callback did not preserve custody error: %v", err)
	}
	callbackErr = nil
	if err := complete(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if len(verified) != 2 || wakes != 0 {
		t.Fatalf("pending verification/wakes = %d/%d", len(verified), wakes)
	}
	for _, got := range verified {
		if got.SessionID != input.SessionID || got.MessageID != input.MessageID || got.Sequence != input.Sequence || got.State != durableflow.InputProcessAccepted {
			t.Fatalf("pending verification lost exact tuple: %+v", got)
		}
	}
	for _, change := range []func(*turnprocessing.DurableInputProcessReceipt){
		func(r *turnprocessing.DurableInputProcessReceipt) { r.SessionID = "other" },
		func(r *turnprocessing.DurableInputProcessReceipt) { r.MessageID = "other" },
		func(r *turnprocessing.DurableInputProcessReceipt) { r.Sequence++ },
		func(r *turnprocessing.DurableInputProcessReceipt) { r.Accepted = false },
	} {
		bad := receipt
		change(&bad)
		if err := complete(ctx, bad); !errors.Is(err, durableflow.ErrInvalidHandoff) {
			t.Fatalf("invalid pending tuple accepted: %v", err)
		}
	}
	if len(verified) != 2 || wakes != 0 {
		t.Fatal("invalid tuple reached durable callback or woke dispatcher")
	}
	receipt.Completion = turnprocessing.DurableInputSucceeded
	for range 2 {
		if err := complete(ctx, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if wakes != 1 {
		t.Fatalf("known terminal wakes = %d, want 1", wakes)
	}
}

func TestAwaitingRecoveryErrorKeepsExactObservedAcceptance(t *testing.T) {
	input := durableflow.ProviderInput{SessionID: "session", MessageID: "message", Sequence: 7}
	fault := errors.New("synthetic acceptance write failure")
	for _, valid := range []bool{true, false} {
		p := durableinputbridge.New(processorFunc(func(context.Context, turnprocessing.DurableLeasedInput, turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error) {
			r := turnprocessing.DurableInputProcessReceipt{SessionID: "session", MessageID: "message", Sequence: 7, Accepted: true, Completion: turnprocessing.DurableInputAwaitingRecovery}
			if !valid {
				r.Sequence++
			}
			return r, fault
		}))
		result, err := p.Process(context.Background(), input, durableflow.InputProcessCallbacks{OnAccepted: func(context.Context, durableflow.HandoffResult) error { return nil }})
		if !errors.Is(err, fault) || (result.State == durableflow.InputProcessAccepted) != valid {
			t.Fatalf("valid=%t observed acceptance=%+v err=%v", valid, result, err)
		}
	}
}
