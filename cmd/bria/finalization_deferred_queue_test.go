package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

type deferredQueueFailure struct {
	entered, release chan struct{}
	once             sync.Once
}

func (f *deferredQueueFailure) fail(ctx context.Context) error {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
		return errors.New("synthetic finalization persistence failure")
	case <-ctx.Done():
		return ctx.Err()
	}
}

type deferredQueueHistory struct {
	*storage.SessionStore
	failure *deferredQueueFailure
}

func (s deferredQueueHistory) AppendCardHistory(ctx context.Context, _ domain.SessionID, _ string) error {
	return s.failure.fail(ctx)
}

type deferredQueueAttachments struct{ failure *deferredQueueFailure }

func (deferredQueueAttachments) MarkAccepted(context.Context, turnprocessing.AttachmentReceipt) error {
	return nil
}
func (a deferredQueueAttachments) MarkCompleted(ctx context.Context, _ turnprocessing.AttachmentReceipt) error {
	return a.failure.fail(ctx)
}

type deferredQueueProvider struct{ submitted chan string }

func (deferredQueueProvider) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unexpected non-durable submit")
}
func (p deferredQueueProvider) SubmitWithCallbacks(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	p.submitted <- callbacks.MessageID
	if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	if callbacks.MessageID == "deferred-A" {
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed, ErrorCode: sessionruntime.ErrorProvider}, sessionruntime.ErrTurnFailed
	}
	return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "recovered B final"}, nil
}
func (p deferredQueueProvider) SubmitPreparedWithCallbacks(ctx context.Context, id domain.SessionID, input turnprocessing.PreparedInput, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return p.SubmitWithCallbacks(ctx, id, input.Text, cb)
}

// The observer only exposes arrival at the real controller after the real flow
// has acquired the exact B lease. It does not change receipts or callbacks.
type deferredQueueController struct {
	*telegramcontroller.Controller
	entered chan telegramcontroller.DurableLeasedInput
}

func (c deferredQueueController) ProcessDurableInput(ctx context.Context, input telegramcontroller.DurableLeasedInput, callbacks telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
	if input.MessageID == "deferred-B" {
		c.entered <- input
	}
	return c.Controller.ProcessDurableInput(ctx, input, callbacks)
}

func TestFinalizationAwaitingRecoveryReleasesExactlyUnsentQueuedInput(t *testing.T) {
	for _, mode := range []string{"history", "attachment-custody"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			store, sessions := acceptedProofFixture(t)
			id := sessions[0].ID()
			failure := &deferredQueueFailure{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(failure.release) }) }
			defer release()
			lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			provider := deferredQueueProvider{submitted: make(chan string, 8)}
			notices := make(chan telegramcontroller.Notification, 16)
			options := telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle}
			if mode == "history" {
				options.UIState = deferredQueueHistory{SessionStore: store, failure: failure}
			} else {
				options.Attachments = deferredQueueAttachments{failure: failure}
			}
			controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, provider,
				archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error { notices <- n; return nil }), options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = controller.Close(context.Background()) }()
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "deferred-worker", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			var attachments []messagejournal.AttachmentRef
			if mode == "attachment-custody" {
				attachments = []messagejournal.AttachmentRef{{Reference: "synthetic-photo", Size: 3, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}
			}
			if _, err := flow.EnqueueInputWithAttachments(ctx, string(id), "deferred-A", []byte("synthetic A"), attachments); err != nil {
				t.Fatal(err)
			}
			observer := deferredQueueController{Controller: controller, entered: make(chan telegramcontroller.DurableLeasedInput, 4)}
			processor := durablecomposition.NewControllerInputProcessor(observer)
			if got, err := flow.ProcessNextInput(ctx, string(id), processor); err != nil || got.State != durableflow.InputProcessAccepted {
				t.Fatalf("A acceptance=%+v err=%v", got, err)
			}
			select {
			case <-failure.entered:
			case <-ctx.Done():
				t.Fatal("A finalization failure barrier not reached")
			}
			current, err := store.Load(ctx, id)
			if err != nil || current.Status() != domain.SessionReady {
				t.Fatalf("at A finalization barrier session=%s err=%v, want Ready", current.Status(), err)
			}
			if got := <-provider.submitted; got != "deferred-A" {
				t.Fatalf("first provider input=%s", got)
			}
			queued, err := flow.EnqueueInput(ctx, string(id), "deferred-B", []byte("synthetic B"))
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				receipt durableflow.InputProcessResult
				err     error
			}
			returned := make(chan result, 1)
			go func() {
				receipt, err := flow.ProcessNextInput(ctx, string(id), processor)
				returned <- result{receipt, err}
			}()
			select {
			case input := <-observer.entered:
				if input.SessionID != id || input.MessageID != "deferred-B" || input.Sequence != queued.Sequence || string(input.Payload) != "synthetic B" {
					t.Fatalf("leased controller input=%+v", input)
				}
			case <-ctx.Done():
				t.Fatal("leased B did not enter real controller")
			}
			inputs := terminalJournalInputs(t, ctx, journalPath, id)
			if len(inputs) != 2 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputPending || inputs[1].Lease.Owner != "deferred-worker" {
				t.Fatalf("B must be physically leased during A finalization: %+v", inputs)
			}
			select {
			case got := <-returned:
				t.Fatalf("B returned before prior finalization: %+v", got)
			case got := <-provider.submitted:
				t.Fatalf("B reached provider during finalization: %s", got)
			case <-time.After(20 * time.Millisecond):
			}
			release()
			select {
			case got := <-returned:
				if got.err != nil || string(got.receipt.State) != "deferred" || got.receipt.SessionID != string(id) || got.receipt.MessageID != "deferred-B" || got.receipt.Sequence != queued.Sequence {
					t.Fatalf("definitely unsent B must return exact deferred result: %+v err=%v", got.receipt, got.err)
				}
			case <-ctx.Done():
				t.Fatal("B did not defer while accepted A awaited recovery")
			}
			waitDeferredQueuePhase(t, ctx, journalPath, id, 0, messagejournal.InputAccepted)
			inputs = terminalJournalInputs(t, ctx, journalPath, id)
			if len(inputs) != 2 || inputs[0].MessageID != "deferred-A" || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].MessageID != "deferred-B" || inputs[1].Sequence != queued.Sequence || inputs[1].Phase != messagejournal.InputPending || inputs[1].Lease != (messagejournal.Lease{}) || string(inputs[1].Payload) != "synthetic B" {
				t.Fatalf("physical deferred queue must preserve Accepted A / unleased exact Pending B: %+v", inputs)
			}
			if got, err := flow.ProcessNextInput(ctx, string(id), processor); err != nil || got.State != durableflow.InputProcessDeferred {
				t.Fatalf("Accepted A did not defer subsequent root dispatch: %+v err=%v", got, err)
			}
			select {
			case got := <-provider.submitted:
				t.Errorf("unsent B was submitted: %s", got)
			default:
			}
			for len(notices) != 0 {
				if n := <-notices; n.Kind == telegramcontroller.NotificationFinal {
					t.Errorf("unexpected final notification: %+v", n)
				}
			}

			// Commit the exact synthetic provider failure proof at the journal's
			// real recovery boundary. Native receipt/JSONL proof construction is
			// covered separately by terminal_native_recovery_test.go.
			if _, err := journal.ResolveAcceptedInput(ctx, string(id), "deferred-A", inputs[0].Sequence, messagejournal.InputTerminalFailed); err != nil {
				t.Fatal(err)
			}
			// Journal settlement plus a plain refresh cannot supersede the old
			// blocked signal while the original runtime generation remains Ready.
			controller.RefreshRecoveryCard(ctx, id)
			if got, err := flow.ProcessNextInput(ctx, string(id), processor); err != nil || got.State != durableflow.InputProcessDeferred {
				t.Fatalf("same-generation refresh admitted blocked input: %+v err=%v", got, err)
			}
			inputs = terminalJournalInputs(t, ctx, journalPath, id)
			if inputs[0].Phase != messagejournal.InputTerminalFailed || inputs[1].Phase != messagejournal.InputPending || inputs[1].Lease != (messagejournal.Lease{}) {
				t.Fatalf("same-generation deferral lost settled A or pending B: %+v", inputs)
			}
			select {
			case got := <-provider.submitted:
				t.Fatalf("same-generation refresh submitted %s", got)
			default:
			}
			current, err = store.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			awaiting, err := current.AwaitRecovery()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Replace(ctx, current, awaiting); err != nil {
				t.Fatal(err)
			}
			binding, bound := current.Binding()
			if !bound {
				t.Fatal("prior runtime binding missing")
			}
			binding.Generation++
			recovered, err := awaiting.Recovered(binding, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Replace(ctx, awaiting, recovered); err != nil {
				t.Fatal(err)
			}
			// Keep the same controller/flow: this is higher-generation admission,
			// not a replacement controller that silently discards its signal.
			controller.RefreshRecoveryCard(ctx, id)
			got, err := flow.ProcessNextInput(ctx, string(id), processor)
			if err != nil || got.SessionID != string(id) || got.MessageID != "deferred-B" || got.Sequence != queued.Sequence || got.State != durableflow.InputProcessAccepted && got.State != durableflow.InputProcessCompleted {
				t.Fatalf("committed exact recovery did not continue original B: %+v err=%v", got, err)
			}
			waitDeferredQueuePhase(t, ctx, journalPath, id, 1, messagejournal.InputCompleted)
			if err := controller.Close(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-provider.submitted:
				if got != "deferred-B" {
					t.Fatalf("recovery replayed %s instead of B", got)
				}
			default:
				t.Fatal("recovered B never reached provider")
			}
			select {
			case got := <-provider.submitted:
				t.Fatalf("duplicate recovered provider input: %s", got)
			default:
			}
			inputs = terminalJournalInputs(t, ctx, journalPath, id)
			if inputs[0].Phase != messagejournal.InputTerminalFailed || inputs[1].Phase != messagejournal.InputCompleted || inputs[1].Sequence != queued.Sequence {
				t.Fatalf("reopened recovered queue=%+v", inputs)
			}
			reopened, err := storage.OpenSessionStore(filepath.Join(sessions[0].Workdir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			stored, err := reopened.Load(ctx, id)
			storedBinding, _ := stored.Binding()
			if err != nil || stored.Status() != domain.SessionReady || storedBinding != binding {
				t.Fatalf("physical recovered runtime=%s binding=%+v err=%v", stored.Status(), storedBinding, err)
			}
			blocks, err := reopened.LoadCardTranscript(ctx, id, true)
			if err != nil {
				t.Fatal(err)
			}
			finals := 0
			for _, block := range blocks {
				if block.Kind == "final" {
					finals++
					if block.Text != "recovered B final" {
						t.Errorf("unexpected persisted final=%q", block.Text)
					}
				}
			}
			if finals != 1 {
				t.Errorf("persisted recovered B finals=%d want=1", finals)
			}
			finalNotices := 0
			for len(notices) != 0 {
				if n := <-notices; n.Kind == telegramcontroller.NotificationFinal {
					finalNotices++
					if n.OperationID != "deferred-B:final" || n.Text != "recovered B final" {
						t.Errorf("wrong recovery final notification: %+v", n)
					}
				}
			}
			if finalNotices != 1 {
				t.Errorf("recovered B final notifications=%d want=1", finalNotices)
			}
			if _, err := journal.RetryInput(ctx, string(id), "deferred-A"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
				t.Fatalf("recovered A replay allowed: %v", err)
			}
			if _, err := flow.ProcessNextInput(ctx, string(id), processor); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("duplicate dispatch after recovery: %v", err)
			}
		})
	}
}

func waitDeferredQueuePhase(t *testing.T, ctx context.Context, path string, id domain.SessionID, index int, want messagejournal.InputPhase) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		inputs := terminalJournalInputs(t, ctx, path, id)
		if len(inputs) == 2 && inputs[index].Phase == want {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("journal[%d] did not reach %s: %+v", index, want, inputs)
		}
	}
}
