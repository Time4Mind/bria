package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
)

type recoveryRuntime struct {
	mu        sync.Mutex
	starts    int
	reads     int
	waits     int
	messageID string
	workdir   string
	prior     domain.ProviderBinding
}

func (runtime *recoveryRuntime) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.starts++
	if request.PriorBinding == nil || *request.PriorBinding != runtime.prior || request.Workdir != runtime.workdir {
		return domain.ProviderBinding{}, errors.New("inexact recovery request")
	}
	return domain.ProviderBinding{Provider: runtime.prior.Provider, SessionID: runtime.prior.SessionID, Generation: runtime.prior.Generation + 1}, nil
}

func (*recoveryRuntime) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

func (*recoveryRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unexpected submit")
}

func (runtime *recoveryRuntime) Wait(ctx context.Context, _ domain.SessionID, _ domain.ProviderBinding) error {
	runtime.mu.Lock()
	runtime.waits++
	runtime.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (runtime *recoveryRuntime) ReadAcceptedTurns(_ context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.reads++
	if request.Binding != runtime.prior || request.Workdir != runtime.workdir || request.Provider != runtime.prior.Provider {
		return sessionruntime.AcceptedTurnReconciliation{}, errors.New("inexact accepted-turn read")
	}
	return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{
		MessageID: runtime.messageID, Outcome: sessionruntime.AcceptedTurnCompleted,
	}}}, nil
}

func TestRunReconcilesAcceptedCodexTurnBeforeOneExactResume(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:accepted-recovery-secret")
	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = domain.SessionID("00000000-0000-4000-8000-000000000094")
	starting, err := domain.NewStartingSession(sessionID, "accepted-recovery", "local", domain.ProviderCodex, temporary)
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := state.PutStartingIfAbsent(context.Background(), starting); err != nil || !inserted {
		t.Fatalf("persist starting session = %t, %v", inserted, err)
	}
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-recovery", Generation: 4}
	ready, err := starting.Ready(prior)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Replace(context.Background(), starting, ready); err != nil {
		t.Fatal(err)
	}
	running, err := ready.StartWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Replace(context.Background(), ready, running); err != nil {
		t.Fatal(err)
	}

	journal, err := messagejournal.Open(statePath+".message-journal.json", messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "local", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	const messageID = "telegram-update:91"
	receipt, err := flow.EnqueueInput(context.Background(), string(sessionID), messageID, []byte("already accepted"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LeaseNextInput(context.Background(), string(sessionID), "local", time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.RecordLeasedInputAccepted(context.Background(), string(sessionID), messageID, receipt.Sequence); err != nil {
		t.Fatal(err)
	}

	runtime := &recoveryRuntime{messageID: messageID, workdir: temporary, prior: prior}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	dependencies := testCommandDependencies(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		case 2:
			return telegramResponse(`{"ok":true,"result":[]}`), nil
		default:
			cancel()
			return nil, context.Canceled
		}
	})})
	dependencies.composeRuntime = func(config.Config, []string, string, sessionruntime.Options) (providerRuntime, error) {
		return runtime, nil
	}
	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("run exit code = %d, stderr = %q", code, stderr.String())
	}
	current, err := state.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	binding, bound := current.Binding()
	inputs, err := journal.Inputs(context.Background(), string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	starts, reads := runtime.starts, runtime.reads
	runtime.mu.Unlock()
	if starts != 1 || reads != 1 || current.Status() != domain.SessionReady || !bound || binding.Generation != 5 ||
		len(inputs) != 1 || inputs[0].Phase != messagejournal.InputCompleted {
		t.Fatalf("starts/reads=%d/%d session=%#v inputs=%#v", starts, reads, current, inputs)
	}
}

var _ sessionruntime.ProcessSupervisor = (*recoveryRuntime)(nil)
var _ sessionruntime.AcceptedTurnReader = (*recoveryRuntime)(nil)
