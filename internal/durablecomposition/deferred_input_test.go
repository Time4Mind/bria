package durablecomposition_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

type deferredAdmission struct{ attempted []string }

func (p *deferredAdmission) Process(_ context.Context, input durableflow.ProviderInput, _ durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
	p.attempted = append(p.attempted, input.MessageID)
	return durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessDeferred}, nil
}

func TestDispatcherStopsAfterKnownUnsentDeferralWithoutRetryingSameCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "c"} {
		if _, err := flow.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
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
	processor := &deferredAdmission{}
	dispatcher := durablecomposition.InputDispatcher{Flow: flow, Processor: processor, Sessions: readySessions{session}}
	if err := dispatcher.ProcessReadySession(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if len(processor.attempted) != 1 || processor.attempted[0] != "b" {
		t.Fatalf("deferred admission retried or overtaken: %v", processor.attempted)
	}
	inputs, err := journal.Inputs(ctx, "s")
	if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputPending || inputs[0].Lease.Owner != "" || inputs[1].Phase != messagejournal.InputPending {
		t.Fatalf("deferral changed custody: %#v %v", inputs, err)
	}
}

func TestControllerDeferredReceiptPreservesPendingJournalBehindFailedPrior(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := flow.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := journal.LeaseNextInput(ctx, "s", "worker", time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInputAccepted(ctx, "s", "a", "worker"); err != nil {
		t.Fatal(err)
	}
	processor := durablecomposition.NewControllerInputProcessor(asyncProcessor(func(ctx context.Context, input telegramcontroller.DurableLeasedInput, _ telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
		if input.MessageID != "b" {
			t.Fatalf("unexpected admission: %#v", input)
		}
		if _, err := journal.MarkInputUnknown(ctx, "s", "a"); err != nil {
			t.Fatal(err)
		}
		return telegramcontroller.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}, turnprocessing.ErrInputDeferred
	}))
	result, err := flow.ProcessNextInput(ctx, "s", processor)
	if err != nil || result.State != durableflow.InputProcessDeferred {
		t.Fatalf("deferred bridge->flow: %#v %v", result, err)
	}
	journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := journal.Inputs(ctx, "s")
	if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputPending || inputs[1].Lease.Owner != "" {
		t.Fatalf("known-unsent B not pending: %#v %v", inputs, err)
	}
	custody := durablecomposition.InputCustody{Flow: flow}
	if err := custody.CheckRootInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: "s", MessageID: "b", Sequence: 2}); err != nil {
		t.Fatalf("accepted A blocked: %v", err)
	}
}
