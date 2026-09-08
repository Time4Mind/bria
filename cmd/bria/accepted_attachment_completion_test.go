package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

type attachmentAcceptanceFailure struct {
	entered chan turnprocessing.AttachmentReceipt
	release chan struct{}
}

func (f attachmentAcceptanceFailure) MarkAccepted(ctx context.Context, receipt turnprocessing.AttachmentReceipt) error {
	f.entered <- receipt
	select {
	case <-f.release:
		return errors.New("synthetic attachment acceptance storage failure")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (attachmentAcceptanceFailure) MarkCompleted(context.Context, turnprocessing.AttachmentReceipt) error {
	return nil
}

// Provider fake preserves the actual callback error instead of synthesizing a
// terminal. Controller, turnprocessing, lifecycle and durable journal are real.
type attachmentAcceptanceProvider struct {
	inputs chan turnprocessing.PreparedInput
}

func (attachmentAcceptanceProvider) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unexpected unstructured submit")
}

func (p attachmentAcceptanceProvider) SubmitPreparedWithCallbacks(_ context.Context, _ domain.SessionID, input turnprocessing.PreparedInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	p.inputs <- input
	return sessionruntime.TurnResult{}, callbacks.OnAccepted(callbacks.MessageID)
}

// Observe the real controller's public asynchronous callback, forwarding it
// unchanged to the real durable adapter. Journal state alone could otherwise
// be satisfied by a synchronous unknown return without OnCompleted ever firing.
type attachmentCompletionObserver struct {
	*telegramcontroller.Controller
	completed chan telegramcontroller.DurableInputProcessReceipt
}

func (o attachmentCompletionObserver) ProcessDurableInput(ctx context.Context, input telegramcontroller.DurableLeasedInput, callbacks telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
	forward := callbacks.OnCompleted
	callbacks.OnCompleted = func(ctx context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
		err := forward(ctx, receipt)
		o.completed <- receipt
		return err
	}
	return o.Controller.ProcessDurableInput(ctx, input, callbacks)
}

func TestAcceptedAttachmentFailureRemainsAwaitingRecoveryWithoutReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, sessions := acceptedProofFixture(t)
	id := sessions[0].ID()
	lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	custody := attachmentAcceptanceFailure{entered: make(chan turnprocessing.AttachmentReceipt, 1), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(custody.release) }) }
	defer release()
	provider := attachmentAcceptanceProvider{inputs: make(chan turnprocessing.PreparedInput, 4)}
	var finals atomic.Int32
	controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, provider,
		archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error {
			if n.Kind == telegramcontroller.NotificationFinal {
				finals.Add(1)
			}
			return nil
		}), telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle, Attachments: custody})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); _ = controller.Close(context.Background()) }()
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "attachment-test", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	const messageID = "accepted-attachment"
	const reference = "synthetic-attachment"
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	queued, err := flow.EnqueueInputWithAttachments(ctx, string(id), messageID, []byte("synthetic attachment prompt"), []messagejournal.AttachmentRef{{Reference: reference, Size: 3, SHA256: digest}})
	if err != nil {
		t.Fatal(err)
	}
	observer := attachmentCompletionObserver{Controller: controller, completed: make(chan telegramcontroller.DurableInputProcessReceipt, 4)}
	processor := durablecomposition.NewControllerInputProcessor(observer)
	result, err := flow.ProcessNextInput(ctx, string(id), processor)
	if err != nil || result.State != durableflow.InputProcessAccepted {
		t.Fatalf("early processing=%+v err=%v; want pending accepted", result, err)
	}
	select {
	case got := <-custody.entered:
		binding, _ := sessions[0].Binding()
		want := turnprocessing.AttachmentReceipt{Reference: reference, ProviderSession: binding.SessionID, MessageID: messageID}
		if got != want {
			t.Fatalf("attachment custody identity=%+v want=%+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("attachment acceptance boundary not reached")
	}
	input := <-provider.inputs
	if len(input.Attachments) != 1 || input.Attachments[0] != (turnprocessing.AttachmentRef{Reference: reference, Size: 3, SHA256: digest}) {
		t.Fatalf("provider attachment=%+v", input.Attachments)
	}
	assertTerminalJournalPhase(t, ctx, journalPath, id, []string{messageID}, string(messagejournal.InputAccepted))
	select {
	case receipt := <-observer.completed:
		t.Fatalf("completion before attachment outcome: %+v", receipt)
	default:
	}
	release()
	select {
	case receipt := <-observer.completed:
		if !receipt.Accepted || string(receipt.Completion) != "awaiting_recovery" || receipt.SessionID != id || receipt.MessageID != messageID || receipt.Sequence != queued.Sequence {
			t.Fatalf("actual controller OnCompleted=%+v, want exact accepted/awaiting_recovery", receipt)
		}
	case <-ctx.Done():
		t.Fatal("actual controller did not invoke OnCompleted after attachment acceptance failure")
	}
	assertTerminalJournalPhase(t, ctx, journalPath, id, []string{messageID}, string(messagejournal.InputAccepted))
	if _, err := flow.ProcessNextInput(ctx, string(id), processor); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("dispatch awaiting recovery=%v; must not replay", err)
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := store.Load(ctx, id)
	if err != nil || current.Status() != domain.SessionRunning {
		t.Errorf("durable recovery target=%s err=%v, want running", current.Status(), err)
	}
	blocks, err := store.LoadCardTranscript(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if string(block.Kind) == "final" {
			t.Errorf("unexpected stored final: %+v", block)
		}
	}
	if got := finals.Load(); got != 0 {
		t.Errorf("final notifications=%d, want none", got)
	}
	select {
	case extra := <-provider.inputs:
		t.Errorf("unexpected provider replay: %+v", extra)
	default:
	}
	select {
	case extra := <-observer.completed:
		t.Errorf("duplicate terminal callback: %+v", extra)
	default:
	}
}
