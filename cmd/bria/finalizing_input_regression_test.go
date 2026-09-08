package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

// Only provider execution is synthetic. Each call is a completed root turn;
// there is deliberately no current-turn steering port after provider completion.
type finalizingInputProvider struct {
	calls chan telegramcontroller.DurableInputAcceptance
}

func (p finalizingInputProvider) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unexpected non-durable submit")
}

func (p finalizingInputProvider) SubmitWithCallbacks(_ context.Context, id domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	p.calls <- telegramcontroller.DurableInputAcceptance{SessionID: id, MessageID: callbacks.MessageID}
	if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "final-" + callbacks.MessageID}, nil
}

func TestProcessDurableInputWaitsForPreviousFinalizationBeforeNewRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base, sessions := acceptedProofFixture(t)
	id := sessions[0].ID()
	store := &terminalJournalStore{SessionStore: base, entered: make(chan error, 2), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(store.release) }) }
	defer release()
	lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	provider := finalizingInputProvider{calls: make(chan telegramcontroller.DurableInputAcceptance, 2)}
	controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, provider,
		archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }),
		telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); _ = controller.Close(context.Background()) }()
	accepted := make(chan telegramcontroller.DurableInputAcceptance, 2)
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 2)
	callbacks := telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(_ context.Context, receipt telegramcontroller.DurableInputAcceptance) error {
			accepted <- receipt
			return nil
		},
		OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
			completed <- receipt
			return nil
		},
	}
	first := telegramcontroller.DurableLeasedInput{SessionID: id, MessageID: "finalizing-first", Sequence: 1, Payload: []byte("first synthetic prompt")}
	second := telegramcontroller.DurableLeasedInput{SessionID: id, MessageID: "finalizing-second", Sequence: 2, Payload: []byte("second synthetic prompt")}
	receipt, err := controller.ProcessDurableInput(ctx, first, callbacks)
	if err != nil || !receipt.Accepted {
		t.Fatalf("first input acceptance=%v err=%v", receipt.Accepted, err)
	}
	select {
	case err := <-store.entered:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("first completed provider did not reach final-storage barrier")
	}
	current, err := store.Load(ctx, id)
	if err != nil || current.Status() != domain.SessionRunning {
		t.Fatalf("at final-storage barrier: status=%s want Running err=%v", current.Status(), err)
	}
	if call := <-provider.calls; call.SessionID != id || call.MessageID != first.MessageID {
		t.Fatal("first provider call lost exact root identity")
	}
	if got := <-accepted; got.SessionID != id || got.MessageID != first.MessageID || got.Sequence != first.Sequence {
		t.Fatal("first acceptance lost exact leased identity")
	}
	type processResult struct {
		receipt telegramcontroller.DurableInputProcessReceipt
		err     error
	}
	result := make(chan processResult, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		r, err := controller.ProcessDurableInput(ctx, second, callbacks)
		result <- processResult{r, err}
	}()
	<-started
	// The store barrier establishes the interleaving; this bounded observation
	// only checks that the second call does not escape while it remains closed.
	select {
	case got := <-result:
		t.Fatalf("second input returned before previous finalization: accepted=%v completion=%s err=%v; want still pending", got.receipt.Accepted, got.receipt.Completion, got.err)
	case <-provider.calls:
		t.Fatal("second provider root started before previous finalization")
	case <-completed:
		t.Fatal("terminal callback preceded final-storage return")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case got := <-result:
		if got.err != nil || !got.receipt.Accepted || got.receipt.SessionID != id || got.receipt.MessageID != second.MessageID || got.receipt.Sequence != second.Sequence {
			t.Fatalf("second input did not succeed after finalization: accepted=%v completion=%s err=%v", got.receipt.Accepted, got.receipt.Completion, got.err)
		}
	case <-ctx.Done():
		t.Fatal("second input remained blocked after finalization")
	}
	select {
	case call := <-provider.calls:
		if call.SessionID != id || call.MessageID != second.MessageID {
			t.Fatal("second provider root lost exact identity")
		}
	case <-ctx.Done():
		t.Fatal("second input never reached provider as a new root")
	}
	select {
	case got := <-accepted:
		if got.SessionID != id || got.MessageID != second.MessageID || got.Sequence != second.Sequence {
			t.Fatal("second acceptance lost exact leased identity")
		}
	case <-ctx.Done():
		t.Fatal("second exact acceptance missing")
	}
	seen := make(map[string]bool)
	for range 2 {
		select {
		case got := <-completed:
			wantSequence := map[string]uint64{first.MessageID: 1, second.MessageID: 2}[got.MessageID]
			if got.SessionID != id || wantSequence == 0 || got.Sequence != wantSequence || !got.Accepted || got.Completion != telegramcontroller.DurableInputSucceeded || seen[got.MessageID] {
				t.Fatal("terminal callbacks must succeed exactly once for each leased input")
			}
			seen[got.MessageID] = true
		case <-ctx.Done():
			t.Fatal("both inputs did not eventually complete")
		}
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(filepath.Join(sessions[0].Workdir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	current, err = reopened.Load(ctx, id)
	if err != nil || current.Status() != domain.SessionReady {
		t.Fatalf("physical terminal state=%s want Ready err=%v", current.Status(), err)
	}
	history, err := reopened.LoadCardHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []telegramcontroller.DurableLeasedInput{first, second} {
		if strings.Count(strings.Join(history, "\n"), "final-"+input.MessageID) != 1 {
			t.Fatal("physical history must contain each completed root final exactly once")
		}
	}
}
