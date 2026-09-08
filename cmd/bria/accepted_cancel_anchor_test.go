package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

type acceptedCancelAnchorStore struct {
	*storage.SessionStore
	message          string
	entered, release chan struct{}
	once             sync.Once
}

func (s *acceptedCancelAnchorStore) SetCardPrompt(ctx context.Context, id domain.SessionID, message, entry string) error {
	if message == s.message && strings.Contains(entry, "👨‍💻") {
		s.once.Do(func() { close(s.entered) })
		<-s.release // Production intentionally gives this write WithoutCancel.
	}
	return s.SessionStore.SetCardPrompt(ctx, id, message, entry)
}

func TestAcceptedProcessCancellationDuringAnchorWriteRetainsExactCustody(t *testing.T) {
	for _, target := range []string{"cancel-main", "cancel-steer"} {
		t.Run(target, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state, sessions := acceptedProofFixture(t)
			id := sessions[0].ID()
			prompts := &acceptedCancelAnchorStore{SessionStore: state, message: target, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(prompts.release) }) }
			defer release()
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestAcceptedTerminalJournalAdapterProcess$"}, Env: []string{"BRIA_TERMINAL_JOURNAL_HELPER=eof"}}}, sessionruntime.Options{GracefulCloseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			request := app.StartSessionRequest{SessionID: id, ComputerID: "local", Provider: domain.ProviderCodex, Workdir: sessions[0].Workdir(), Mode: app.SessionStartNew}
			binding, err := starter.Start(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			defer starter.Abort(context.Background(), request, binding)
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "cancel-worker", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			lifecycle, err := app.NewSessionTurnLifecycle(state, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, state, starter, archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Recovered: sessions, UIState: prompts, TurnLifecycle: lifecycle, DurableInput: durablecomposition.InputCustody{Flow: flow}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = controller.Close(context.Background()) }()
			processor := durablecomposition.NewControllerInputProcessor(controller)
			messages := []string{}
			if target == "cancel-steer" {
				messages = append(messages, "cancel-main")
				if _, err := flow.EnqueueInput(ctx, string(id), messages[0], []byte("synthetic main")); err != nil {
					t.Fatal(err)
				}
				if got, err := flow.ProcessNextInput(ctx, string(id), processor); err != nil || got.State != durableflow.InputProcessAccepted {
					t.Fatalf("main acceptance=%+v err=%v", got, err)
				}
			}
			messages = append(messages, target)
			if _, err := flow.EnqueueInput(ctx, string(id), target, []byte("synthetic target")); err != nil {
				t.Fatal(err)
			}
			processCtx, cancelProcess := context.WithCancel(ctx)
			defer cancelProcess()
			done := make(chan durableflow.InputProcessResult, 1)
			go func() { got, _ := flow.ProcessNextInput(processCtx, string(id), processor); done <- got }()
			select {
			case <-prompts.entered:
			case <-ctx.Done():
				t.Fatal("accepted prompt write not reached")
			}
			// This exact native ACK must be durably recorded before ancillary
			// prompt I/O can block and before its Process caller is cancelled.
			before := terminalJournalInputs(t, ctx, path, id)
			if before[len(before)-1].Phase != messagejournal.InputAccepted {
				t.Errorf("ACK reached blocked anchor before custody: phase=%s", before[len(before)-1].Phase)
			}
			cancelProcess()
			assertTerminalJournalPhase(t, ctx, path, id, messages, string(messagejournal.InputAccepted))
			release()
			select {
			case got := <-done:
				if got.State != durableflow.InputProcessAccepted {
					t.Errorf("cancelled accepted process=%+v", got)
				}
			case <-ctx.Done():
				t.Fatal("cancelled process did not finish")
			}
			if err := controller.Close(ctx); err != nil {
				t.Fatal(err)
			}
			assertTerminalJournalPhase(t, ctx, path, id, messages, string(messagejournal.InputAccepted))
		})
	}
}
