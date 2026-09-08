package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

// Only the final-storage boundary is faulted/gated. Runtime protocol, lifecycle,
// controller processing, journal callbacks and all successful storage are real.
type terminalJournalStore struct {
	*storage.SessionStore
	fail    bool
	entered chan error
	release chan struct{}
}

func (s *terminalJournalStore) RestoreAcceptedFinal(ctx context.Context, id domain.SessionID, messageID, final string) error {
	_, err := s.restoreFinalAttempt(ctx, func() (bool, error) {
		err := s.SessionStore.RestoreAcceptedFinal(ctx, id, messageID, final)
		return err == nil, err
	})
	return err
}

func (s *terminalJournalStore) RestoreAcceptedFinalForSession(ctx context.Context, expected domain.Session, messageID, final string) (bool, error) {
	return s.restoreFinalAttempt(ctx, func() (bool, error) {
		writer, ok := any(s.SessionStore).(interface {
			RestoreAcceptedFinalForSession(context.Context, domain.Session, string, string) (bool, error)
		})
		if !ok {
			return false, errors.New("atomic final fixture backend not available")
		}
		return writer.RestoreAcceptedFinalForSession(ctx, expected, messageID, final)
	})
}

func (s *terminalJournalStore) restoreFinalAttempt(ctx context.Context, save func() (bool, error)) (bool, error) {
	var err error
	saved := false
	if s.fail {
		err = errors.New("synthetic final storage failure")
	} else {
		saved, err = save()
		if !saved && err == nil {
			return false, nil
		}
	}
	// Retry attempts must never block on an undrained fixture observation.
	select {
	case s.entered <- err:
	default:
	}
	select {
	case <-s.release:
		return saved, err
	case <-ctx.Done():
		return saved, ctx.Err()
	}
}

func TestAcceptedTerminalJournalWaitsForPersistedFinal(t *testing.T) {
	for _, mode := range []string{"completed", "steer", "eof", "store-failure", "store-failure-closing"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			base, sessions := acceptedProofFixture(t)
			id := sessions[0].ID()
			store := &terminalJournalStore{SessionStore: base, fail: strings.HasPrefix(mode, "store-failure"), entered: make(chan error, 1), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(store.release) }) }
			defer release()
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestAcceptedTerminalJournalAdapterProcess$"}, Env: []string{"BRIA_TERMINAL_JOURNAL_HELPER=" + mode}},
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
			storedBinding, _ := sessions[0].Binding()
			if binding != storedBinding {
				t.Fatalf("runtime binding=%+v stored=%+v", binding, storedBinding)
			}
			lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			closer, err := app.NewSessionCloser(store, starter, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			var finals atomic.Int32
			controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, starter,
				archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error {
					if n.Kind == telegramcontroller.NotificationFinal {
						finals.Add(1)
					}
					return nil
				}), telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle, SessionCloser: closer})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = controller.Close(context.Background()) }()
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "terminal-test", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			processor := durablecomposition.NewControllerInputProcessor(controller)
			messages := []string{"terminal-parent"}
			process := func(message string) {
				t.Helper()
				if _, err := flow.EnqueueInput(ctx, string(id), message, []byte("synthetic "+message)); err != nil {
					t.Fatal(err)
				}
				result, err := flow.ProcessNextInput(ctx, string(id), processor)
				if err != nil || result.State != durableflow.InputProcessAccepted || result.MessageID != message {
					t.Fatalf("early ProcessNextInput=%+v err=%v; want exact accepted, not completed", result, err)
				}
			}
			process(messages[0])
			if mode == "steer" {
				messages = append(messages, "terminal-steer")
				process(messages[1])
			}
			assertTerminalJournalPhase(t, ctx, journalPath, id, messages, string(messagejournal.InputAccepted))
			assertTerminalJournalFinal(t, ctx, sessions[0].Workdir(), id, false)
			if mode == "store-failure-closing" {
				if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: id, Choice: 1}); err != nil {
					t.Fatal(err)
				}
			}
			// The synthetic adapter releases its chosen terminal only on this
			// protocol request. No local files, sleeps or provider are used as gates.
			_ = starter.StopCurrent(ctx, id)
			if mode != "eof" {
				select {
				case err := <-store.entered:
					if (err != nil) != store.fail {
						t.Fatalf("final storage error=%v fail=%v", err, store.fail)
					}
				case <-ctx.Done():
					t.Fatal("final storage boundary was not reached")
				}
				// Successful final bytes are physically reopenable while both the
				// parent and any steer remain accepted, before async OnCompleted.
				assertTerminalJournalPhase(t, ctx, journalPath, id, messages, string(messagejournal.InputAccepted))
				assertTerminalJournalFinal(t, ctx, sessions[0].Workdir(), id, !store.fail)
				if got := finals.Load(); got != 0 {
					t.Fatalf("final notification before final-storage commit returned: %d", got)
				}
				release()
			}
			wantPhase := string(messagejournal.InputCompleted)
			wantStatus := domain.SessionReady
			if mode == "eof" || store.fail {
				wantPhase = string(messagejournal.InputUnknown)
				wantStatus = domain.SessionRunning
				if mode == "store-failure-closing" {
					wantStatus = domain.SessionClosingAfterWork
				}
			}
			if store.fail {
				// A persistent storage fault is unresolved, not Unknown, while
				// the controller can retry the already received exact final.
				select {
				case <-store.entered:
				case <-time.After(2 * time.Second):
					t.Fatal("persistent final-save failure was not retried")
				}
				assertTerminalJournalPhase(t, ctx, journalPath, id, messages, string(messagejournal.InputAccepted))
				current, err := store.Load(ctx, id)
				if err != nil || current.Status() != wantStatus {
					t.Fatalf("retrying final-save status=%s want=%s err=%v", current.Status(), wantStatus, err)
				}
			} else {
				waitTerminalJournalPhase(t, ctx, journalPath, id, messages, wantPhase)
			}
			closeStarted := time.Now()
			if err := controller.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if store.fail && time.Since(closeStarted) >= time.Second {
				t.Fatal("shutdown waited for final-save retry backoff instead of cancelling it")
			}
			if store.fail {
				waitTerminalJournalPhase(t, ctx, journalPath, id, messages, wantPhase)
			}
			current, err := store.Load(ctx, id)
			if err != nil || current.Status() != wantStatus {
				t.Errorf("terminal %s: durable session=%s want=%s err=%v", mode, current.Status(), wantStatus, err)
			}
			wantFinal := mode != "eof" && !store.fail
			assertTerminalJournalFinal(t, ctx, sessions[0].Workdir(), id, wantFinal)
			wantNotices := int32(0)
			if wantFinal {
				wantNotices = 1
			}
			if got := finals.Load(); got != wantNotices {
				t.Errorf("final notifications=%d want=%d", got, wantNotices)
			}
			// An unknown accepted input must not become a replayable lease.
			if _, err := flow.ProcessNextInput(ctx, string(id), processor); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Errorf("second dispatch=%v, want no available input (no replay)", err)
			}
			assertTerminalJournalPhase(t, ctx, journalPath, id, messages, wantPhase)
		})
	}
}

func terminalJournalInputs(t *testing.T, ctx context.Context, path string, id domain.SessionID) []messagejournal.Input {
	t.Helper()
	reopened, err := messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := reopened.Inputs(ctx, string(id))
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}

func assertTerminalJournalPhase(t *testing.T, ctx context.Context, path string, id domain.SessionID, messages []string, phase string) {
	t.Helper()
	inputs := terminalJournalInputs(t, ctx, path, id)
	if len(inputs) != len(messages) {
		t.Fatalf("journal entries=%d want=%d", len(inputs), len(messages))
	}
	for i, input := range inputs {
		if input.MessageID != messages[i] || input.Sequence != uint64(i+1) || string(input.Phase) != phase {
			t.Fatalf("journal[%d]=%s/%d/%s want=%s/%d/%s", i, input.MessageID, input.Sequence, input.Phase, messages[i], i+1, phase)
		}
	}
}

func waitTerminalJournalPhase(t *testing.T, ctx context.Context, path string, id domain.SessionID, messages []string, phase string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		inputs := terminalJournalInputs(t, ctx, path, id)
		ready := len(inputs) == len(messages)
		for _, input := range inputs {
			ready = ready && string(input.Phase) == phase
		}
		if ready {
			assertTerminalJournalPhase(t, ctx, path, id, messages, phase)
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("journal did not reach %s: %+v", phase, inputs)
		}
	}
}

func assertTerminalJournalFinal(t *testing.T, ctx context.Context, dir string, id domain.SessionID, want bool) {
	t.Helper()
	reopened, err := storage.OpenSessionStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, block := range blocks {
		if block.Text == "SYNTHETIC_TERMINAL_FINAL" {
			count++
		}
	}
	wantCount := 0
	if want {
		wantCount = 1
	}
	if count != wantCount {
		t.Fatalf("physically stored exact final count=%d want=%d; transcript=%+v", count, wantCount, blocks)
	}
}

// A subprocess protocol fake, not a fake controller or fake runtime acceptance.
// It never launches a provider, accesses live state or uses the network.
func TestAcceptedTerminalJournalAdapterProcess(t *testing.T) {
	mode := os.Getenv("BRIA_TERMINAL_JOURNAL_HELPER")
	if mode == "" {
		return
	}
	emit := func(value any) {
		if json.NewEncoder(os.Stdout).Encode(value) != nil {
			os.Exit(81)
		}
	}
	emit(map[string]any{"protocol": 1, "type": "ready", "provider_session_id": "provider-" + os.Getenv("BRIA_SESSION_ID"), "readiness": "protocol", "authentication": "unknown"})
	scanner := bufio.NewScanner(os.Stdin)
	rootRequest := ""
	for scanner.Scan() {
		var message struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			MessageID string `json:"message_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(82)
		}
		switch message.Type {
		case "submit":
			if rootRequest != "" {
				os.Exit(84)
			}
			rootRequest = message.RequestID
			emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
		case "steer":
			if rootRequest == "" {
				os.Exit(85)
			}
			emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
		case "interrupt":
			if mode == "eof" {
				os.Exit(1)
			}
			emit(map[string]any{"protocol": 1, "type": "final", "request_id": rootRequest, "text": "SYNTHETIC_TERMINAL_FINAL"})
			emit(map[string]any{"protocol": 1, "type": "completed", "request_id": rootRequest, "status": "completed"})
		case "close":
			os.Exit(0)
		default:
			os.Exit(83)
		}
	}
	os.Exit(0)
}
