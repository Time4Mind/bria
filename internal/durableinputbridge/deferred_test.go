package durableinputbridge_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/durableflow"
	"bria/internal/durableinputbridge"
	"bria/internal/turnprocessing"
)

func TestDeferredReceiptRequiresExactUnacceptedSentinel(t *testing.T) {
	for _, mode := range []string{"exact", "accepted", "wrong-session", "wrong-message", "wrong-sequence", "ordinary-error"} {
		t.Run(mode, func(t *testing.T) {
			processor := durableinputbridge.New(processorFunc(func(_ context.Context, input turnprocessing.DurableLeasedInput, _ turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error) {
				receipt := turnprocessing.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}
				err := errors.Join(errors.New("synthetic blocked prior"), turnprocessing.ErrInputDeferred)
				switch mode {
				case "accepted":
					receipt.Accepted = true
				case "wrong-session":
					receipt.SessionID = "other"
				case "wrong-message":
					receipt.MessageID = "other"
				case "wrong-sequence":
					receipt.Sequence++
				case "ordinary-error":
					err = errors.New("ambiguous provider failure")
				}
				return receipt, err
			}))
			got, err := processor.Process(context.Background(), durableflow.ProviderInput{SessionID: "s", MessageID: "m", Sequence: 1}, durableflow.InputProcessCallbacks{OnAccepted: func(context.Context, durableflow.HandoffResult) error { t.Error("deferred input accepted"); return nil }})
			if mode == "exact" {
				if err != nil || got.State != durableflow.InputProcessDeferred {
					t.Fatalf("known-unsent receipt not deferred: %#v %v", got, err)
				}
			} else if err == nil || got.State != durableflow.InputProcessUnknown {
				t.Fatalf("unsafe deferral: %#v %v", got, err)
			}
		})
	}
}
