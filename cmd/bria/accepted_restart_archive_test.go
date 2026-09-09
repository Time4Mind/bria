package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/nativeacceptance"
	"bria/internal/nativereceiptstore"
	"bria/internal/recoverycomposition"
	"bria/internal/recoveryruntime"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
	"bria/internal/supervisioncomposition"
	"bria/internal/telegramcontroller"
)

// Continues the real adapter/controller main+steer EOF fixture after Close.
// Reopen every durable boundary; native evidence is synthetic local JSONL.
func assertAcceptedRestartArchiveLateFinal(t *testing.T, ctx context.Context, original domain.Session, journalPath string, messages []string) {
	t.Helper()
	id, dir := original.ID(), original.Workdir()
	state, err := storage.OpenSessionStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	retained, err := journal.Inputs(ctx, string(id))
	if err != nil {
		t.Fatal(err)
	}
	retained = retained[:len(messages)]
	now, pendingCommit := time.Now(), false
	for _, input := range retained {
		if input.Phase == messagejournal.InputPending {
			pendingCommit = true
			if input.Lease.Until.IsZero() {
				t.Fatal("uncommitted ACK lost its lease")
			}
			now = input.Lease.Until.Add(time.Hour)
		}
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "restarted", LeaseDuration: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	const nextMessage = "after-recovery-B"
	queued, err := flow.EnqueueInput(ctx, string(id), nextMessage, []byte("synthetic B"))
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := original.Binding()
	receipts, transcripts := filepath.Join(dir, "receipts"), filepath.Join(dir, "sessions")
	doc := nativeacceptance.Document{SessionID: binding.SessionID, Receipts: map[string]string{}, TurnIDs: map[string]string{}}
	for i, message := range messages {
		doc.Receipts[message] = "unknown"
		doc.TurnIDs[message] = fmt.Sprintf("exact-turn-%d", i)
	}
	initialDoc := doc
	if pendingCommit {
		// There is no native evidence of either acceptance or completion yet.
		initialDoc = nativeacceptance.Document{SessionID: binding.SessionID, Receipts: map[string]string{}, TurnIDs: map[string]string{}}
	}
	if err := nativereceiptstore.Write(receipts, initialDoc); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(transcripts, 0700); err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]string{"id": binding.SessionID, "cwd": dir}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(transcripts, "rollout-"+binding.SessionID+".jsonl")
	nativeText := string(header) + "\n"
	if err := os.WriteFile(path, []byte(nativeText), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := recoveryruntime.NewNativeWithTranscriptRoot(receipts, domain.ProviderCodex, transcripts)
	if err != nil {
		t.Fatal(err)
	}
	history, err := recoverycomposition.NewReconciler(reader, state)
	if err != nil {
		t.Fatal(err)
	}
	reconciler := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}, FinalRestorer: state}
	runtime := &acceptedRestartRuntime{prior: binding, submitted: make(chan string, 8)}
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{LocalComputerID: "local", Store: state, Waiter: runtime, Restarter: runtime, AcceptedTurns: reconciler, MaxRestartAttempts: 1, SweepInterval: time.Hour, WaitBeforeRetry: func(context.Context, int) error { return nil }, Now: time.Now, Report: func(error) {}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RecoverStartup(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := state.Load(ctx, id)
	if err != nil || current.Status() != domain.SessionAwaitingRecovery || runtime.starts.Load() != 0 {
		t.Fatalf("unproven startup state=%s starts=%d err=%v", current.Status(), runtime.starts.Load(), err)
	}
	assertRetainedRecoveryQueue(t, ctx, journalPath, id, messages, queued.Sequence, messagejournal.InputAccepted, retained...)
	if pendingCommit {
		// Expired lease + new flow/controller must not replay A or skip it for B.
		probe, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, state, runtime, archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Recovered: []domain.Session{original}, UIState: state, DurableInput: durablecomposition.InputCustody{Flow: flow}})
		if err != nil {
			t.Fatal(err)
		}
		dispatcher := durablecomposition.InputDispatcher{Flow: flow, Sessions: state, Processor: durablecomposition.NewControllerInputProcessor(probe)}
		dispatchErr := dispatcher.ProcessReadySession(ctx, id)
		if err := probe.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if len(runtime.submitted) != 0 || dispatchErr != nil {
			t.Fatalf("expired unproven ACK dispatched: err=%v", dispatchErr)
		}
		assertRetainedRecoveryQueue(t, ctx, journalPath, id, messages, queued.Sequence, messagejournal.InputAccepted, retained...)
	}
	closer, err := app.NewSessionCloser(state, runtime, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := closer.Close(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Use the same guarded production resumer as single-machine composition.
	baseResumer, err := app.NewArchivedSessionResumer(state, runtime, domain.SessionLifetimeNever, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	resumer := durablecomposition.GuardedArchivedResumer{Base: baseResumer, Sessions: state, Reconciler: reconciler}
	resumedEarly, resumeErr := resumer.Resume(ctx, id)
	if pendingCommit {
		if resumeErr == nil || runtime.starts.Load() != 0 {
			t.Fatalf("unproven acceptance resumed: starts=%d err=%v", runtime.starts.Load(), resumeErr)
		}
	} else if resumeErr != nil || resumedEarly.Status() != domain.SessionReady || runtime.starts.Load() != 1 || len(runtime.submitted) != 0 {
		t.Fatalf("explicit archive reopen failed or replayed: starts=%d err=%v", runtime.starts.Load(), resumeErr)
	}
	assertRetainedRecoveryQueue(t, ctx, journalPath, id, messages, queued.Sequence, messagejournal.InputAccepted, retained...)
	if err := nativereceiptstore.Write(receipts, doc); err != nil {
		t.Fatal(err)
	}
	for i := range messages {
		nativeText += fmt.Sprintf("{\"type\":\"turn_context\",\"payload\":{\"turn_id\":\"exact-turn-%d\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"phase\":\"final\",\"message\":\"recovered-final-%d\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\",\"turn_id\":\"exact-turn-%d\"}}\n", i, i, i)
	}
	if err := os.WriteFile(path, []byte(nativeText), 0600); err != nil {
		t.Fatal(err)
	}
	recovered := resumedEarly
	if pendingCommit {
		recovered, err = resumer.Resume(ctx, id)
	} else {
		binding, _ = recovered.Binding()
		_, err = reconciler.ReconcileAcceptedTurns(ctx, id, binding)
	}
	if err != nil || recovered.Status() != domain.SessionReady || runtime.starts.Load() != 1 {
		t.Fatalf("proven archived resume=%s starts=%d err=%v", recovered.Status(), runtime.starts.Load(), err)
	}
	assertRetainedRecoveryQueue(t, ctx, journalPath, id, messages, queued.Sequence, messagejournal.InputCompleted)
	// Reconciliation must be idempotent before the only new root B is admitted.
	if _, err := reconciler.ReconcileAcceptedTurns(ctx, id, binding); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := app.NewSessionTurnLifecycle(state, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, state, runtime, archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Recovered: []domain.Session{recovered}, UIState: state, TurnLifecycle: lifecycle, DurableInput: durablecomposition.InputCustody{Flow: flow}})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	processor := durablecomposition.NewControllerInputProcessor(controller)
	got, err := flow.ProcessNextInput(ctx, string(id), processor)
	if err != nil || got.MessageID != nextMessage || got.Sequence != queued.Sequence || got.State != durableflow.InputProcessAccepted && got.State != durableflow.InputProcessCompleted {
		t.Fatalf("queued successor=%+v err=%v", got, err)
	}
	waitTerminalJournalPhase(t, ctx, journalPath, id, append(append([]string{}, messages...), nextMessage), string(messagejournal.InputCompleted))
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-runtime.submitted:
		if got != nextMessage {
			t.Fatalf("replayed old accepted input %s", got)
		}
	default:
		t.Fatal("B never submitted")
	}
	select {
	case got := <-runtime.submitted:
		t.Fatalf("duplicate submission %s", got)
	default:
	}
	if _, err := flow.ProcessNextInput(ctx, string(id), processor); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("duplicate dispatch: %v", err)
	}
	reopened, err := storage.OpenSessionStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, block := range blocks {
		if block.Kind == "final" {
			counts[block.Text]++
		}
	}
	if len(counts) != len(messages)+1 || counts["successor-final"] != 1 {
		t.Fatalf("reopened finals=%v", counts)
	}
	for i := range messages {
		if counts[fmt.Sprintf("recovered-final-%d", i)] != 1 {
			t.Fatalf("missing/duplicate exact recovered final: %v", counts)
		}
	}
}

func assertRetainedRecoveryQueue(t *testing.T, ctx context.Context, path string, id domain.SessionID, messages []string, sequence uint64, phase messagejournal.InputPhase, retained ...messagejournal.Input) {
	t.Helper()
	inputs := terminalJournalInputs(t, ctx, path, id)
	if len(inputs) != len(messages)+1 {
		t.Fatalf("queue entries=%d", len(inputs))
	}
	for i, message := range messages {
		wantPhase := phase
		if len(retained) != 0 {
			wantPhase = retained[i].Phase
			if inputs[i].Lease != retained[i].Lease {
				t.Fatalf("recovery changed retained lease for %s: %+v want %+v", message, inputs[i].Lease, retained[i].Lease)
			}
		}
		if inputs[i].MessageID != message || inputs[i].Sequence != uint64(i+1) || inputs[i].Phase != wantPhase {
			t.Fatalf("retained request %d: %+v", i, inputs[i])
		}
	}
	b := inputs[len(messages)]
	if b.MessageID != "after-recovery-B" || b.Sequence != sequence || b.Phase != messagejournal.InputPending || b.Lease != (messagejournal.Lease{}) {
		t.Fatalf("queued B changed: %+v", b)
	}
}

type acceptedRestartRuntime struct {
	prior     domain.ProviderBinding
	starts    atomic.Int32
	submitted chan string
}

func (r *acceptedRestartRuntime) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	r.starts.Add(1)
	if request.Mode != app.SessionStartResume || request.PriorBinding == nil || *request.PriorBinding != r.prior {
		return domain.ProviderBinding{}, errors.New("inexact resume")
	}
	next := r.prior
	next.Generation++
	return next, nil
}
func (*acceptedRestartRuntime) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}
func (*acceptedRestartRuntime) Wait(context.Context, domain.SessionID, domain.ProviderBinding) error {
	return nil
}
func (*acceptedRestartRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unstructured submit")
}
func (r *acceptedRestartRuntime) SubmitWithCallbacks(_ context.Context, _ domain.SessionID, _ string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	r.submitted <- cb.MessageID
	if err := cb.OnAccepted(cb.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "successor-final"}, nil
}
