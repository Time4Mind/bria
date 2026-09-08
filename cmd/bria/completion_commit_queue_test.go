package main

import (
	"context"
	"errors"
	"os"
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
)

type completionCommitHistoryGate struct {
	*storage.SessionStore
	entered, release chan struct{}
	once             sync.Once
}

type completionCommitController struct {
	*telegramcontroller.Controller
	entered chan struct{}
}

func (c completionCommitController) ProcessDurableInput(ctx context.Context, input telegramcontroller.DurableLeasedInput, callbacks telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
	if input.MessageID == "queue-B" {
		c.entered <- struct{}{}
	}
	return c.Controller.ProcessDurableInput(ctx, input, callbacks)
}

func TestCompletionJournalFailureMainOrSteerBlocksActualRuntimeQueue(t *testing.T) {
	for _, failedMessage := range []string{"queue-A", "queue-steer"} {
		t.Run(failedMessage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			store, sessions := acceptedProofFixture(t)
			id := sessions[0].ID()
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestTerminalQueueAdapterProcess$"}, Env: []string{"BRIA_TERMINAL_QUEUE_HELPER=interrupted"}},
			}, sessionruntime.Options{GracefulCloseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			request := app.StartSessionRequest{SessionID: id, ComputerID: "local", Provider: domain.ProviderCodex, Workdir: sessions[0].Workdir(), Mode: app.SessionStartNew}
			binding, err := starter.Start(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			defer starter.Abort(context.Background(), request, binding)
			runtime := queueTerminalRuntime{Starter: starter, submitted: make(chan string, 16)}
			history := &completionCommitHistoryGate{SessionStore: store, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(history.release) }) }
			defer release()
			lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, runtime,
				archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }),
				telegramcontroller.Options{Recovered: sessions, UIState: history, TurnLifecycle: lifecycle})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = c.Close(context.Background()) }()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			fault := &completionCommitFailJournal{Journal: journal, message: failedMessage, failed: make(chan struct{})}
			flow, err := durableflow.New(fault, nil, nil, durableflow.Options{Owner: "commit-worker", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			observer := completionCommitController{Controller: c, entered: make(chan struct{}, 4)}
			processor := durablecomposition.NewControllerInputProcessor(observer)
			for _, message := range []string{"queue-A", "queue-steer"} {
				if _, err := flow.EnqueueInput(ctx, string(id), message, []byte("synthetic "+message)); err != nil {
					t.Fatal(err)
				}
				if got, err := flow.ProcessNextInput(ctx, string(id), processor); err != nil || got.State != durableflow.InputProcessAccepted {
					t.Fatalf("%s acceptance=%+v err=%v", message, got, err)
				}
			}
			_ = starter.StopCurrent(ctx, id)
			select {
			case <-history.entered:
			case <-ctx.Done():
				t.Fatal("actual runtime terminal did not reach finalization barrier")
			}
			if _, err := flow.EnqueueInput(ctx, string(id), "queue-B", []byte("synthetic queue-B")); err != nil {
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
			case <-observer.entered:
			case <-ctx.Done():
				t.Fatal("B did not enter real controller")
			}
			before := terminalJournalInputs(t, ctx, path, id)
			if len(before) != 3 || before[0].Phase != messagejournal.InputAccepted || before[1].Phase != messagejournal.InputAccepted || before[2].Lease.Owner != "commit-worker" {
				t.Fatalf("need accepted main/steer and leased B: %+v", before)
			}
			release()
			select {
			case <-fault.failed:
			case <-ctx.Done():
				t.Fatal("selected main/steer journal commit was not attempted")
			}
			var got result
			select {
			case got = <-returned:
			case <-ctx.Done():
				t.Fatal("B did not finish deferring")
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			after := terminalJournalInputs(t, ctx, path, id)
			var sent []string
			for len(runtime.submitted) != 0 {
				sent = append(sent, <-runtime.submitted)
			}
			if len(sent) != 2 || sent[0] != "queue-A" || sent[1] != "queue-steer" {
				t.Fatalf("actual runtime inputs=%v after %s commit failure; phases=%s/%s/%s; B result=%s err=%v", sent, failedMessage, after[0].Phase, after[1].Phase, after[2].Phase, got.receipt.State, got.err)
			}
			if got.err != nil || got.receipt.State != durableflow.InputProcessDeferred || got.receipt.MessageID != "queue-B" || got.receipt.Sequence != 3 || after[2].MessageID != "queue-B" || after[2].Phase != messagejournal.InputPending || after[2].Lease != (messagejournal.Lease{}) {
				t.Fatalf("main/steer commit failure did not release exact unsent B: result=%+v err=%v journal=%+v", got.receipt, got.err, after)
			}
		})
	}
}

func (g *completionCommitHistoryGate) AppendCardHistory(ctx context.Context, id domain.SessionID, text string) error {
	if err := g.SessionStore.AppendCardHistory(ctx, id, text); err != nil {
		return err
	}
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type completionCommitFailJournal struct {
	*messagejournal.Journal
	message string
	failed  chan struct{}
	once    sync.Once
}

func (j *completionCommitFailJournal) ResolveAcceptedInput(ctx context.Context, id, message string, sequence uint64, phase messagejournal.InputPhase) (messagejournal.Input, error) {
	if message == j.message {
		j.once.Do(func() { close(j.failed) })
		return messagejournal.Input{}, errors.New("synthetic terminal journal persistence failure")
	}
	return j.Journal.ResolveAcceptedInput(ctx, id, message, sequence, phase)
}

// Sanitized integration regression from the independent review: history is
// durably written, but the exact journal completion fails while B is leased.
func TestCompletionJournalCommitFailureMustNotReleaseWaitingB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, sessions := acceptedProofFixture(t)
	id := sessions[0].ID()
	history := &completionCommitHistoryGate{SessionStore: store, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(history.release) }) }
	defer release()
	lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	provider := deferredQueueProvider{submitted: make(chan string, 8)}
	c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, provider,
		archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }),
		telegramcontroller.Options{Recovered: sessions, UIState: history, TurnLifecycle: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); _ = c.Close(context.Background()) }()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	fault := &completionCommitFailJournal{Journal: journal, message: "deferred-A", failed: make(chan struct{})}
	flow, err := durableflow.New(fault, nil, nil, durableflow.Options{Owner: "commit-worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	observer := deferredQueueController{Controller: c, entered: make(chan telegramcontroller.DurableLeasedInput, 1)}
	processor := durablecomposition.NewControllerInputProcessor(observer)
	if _, err := flow.EnqueueInput(ctx, string(id), "deferred-A", []byte("synthetic A")); err != nil {
		t.Fatal(err)
	}
	a, err := flow.ProcessNextInput(ctx, string(id), processor)
	if err != nil || a.State != durableflow.InputProcessAccepted {
		t.Fatalf("A=%+v err=%v", a, err)
	}
	select {
	case <-history.entered:
	case <-ctx.Done():
		t.Fatal("history barrier not reached")
	}
	if got := <-provider.submitted; got != "deferred-A" {
		t.Fatalf("first provider input=%s", got)
	}
	if _, err := flow.EnqueueInput(ctx, string(id), "deferred-B", []byte("synthetic B")); err != nil {
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
	case <-observer.entered:
	case <-ctx.Done():
		t.Fatal("B not leased")
	}
	before := terminalJournalInputs(t, ctx, path, id)
	if len(before) != 2 || before[0].Phase != messagejournal.InputAccepted || before[1].Lease.Owner != "commit-worker" {
		t.Fatalf("fixture must reach Accepted A / leased B: %+v", before)
	}
	release()
	select {
	case <-fault.failed:
	case <-ctx.Done():
		t.Fatal("journal commit failure not reached")
	}
	var got result
	select {
	case got = <-returned:
	case <-ctx.Done():
		t.Fatal("B did not return")
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	after := terminalJournalInputs(t, ctx, path, id)
	select {
	case sent := <-provider.submitted:
		t.Fatalf("provider received %s despite failed A commit; physical A=%s B=%s; B returned=%s err=%v", sent, after[0].Phase, after[1].Phase, got.receipt.State, got.err)
	default:
	}
	if got.err != nil || got.receipt.State != durableflow.InputProcessDeferred || got.receipt.SessionID != string(id) || got.receipt.MessageID != "deferred-B" || got.receipt.Sequence != 2 || after[1].Phase != messagejournal.InputPending || after[1].Lease != (messagejournal.Lease{}) {
		t.Fatalf("unsent exact B not deferred: %+v err=%v journal=%+v", got.receipt, got.err, after)
	}
}
