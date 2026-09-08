package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

type finalSaveAttempt struct {
	number int
	at     time.Time
	err    error
}

type retryFinalStore struct {
	*storage.SessionStore
	mu           sync.Mutex
	attempts     int
	ambiguous    bool
	observed     chan finalSaveAttempt
	releaseSaved chan struct{}
}

func (s *retryFinalStore) RestoreAcceptedFinal(ctx context.Context, id domain.SessionID, message, final string) error {
	_, err := s.restoreFinalAttempt(ctx, message, func() (bool, error) {
		err := s.SessionStore.RestoreAcceptedFinal(ctx, id, message, final)
		return err == nil, err
	})
	return err
}

func (s *retryFinalStore) RestoreAcceptedFinalForSession(ctx context.Context, expected domain.Session, message, final string) (bool, error) {
	return s.restoreFinalAttempt(ctx, message, func() (bool, error) {
		writer, ok := any(s.SessionStore).(interface {
			RestoreAcceptedFinalForSession(context.Context, domain.Session, string, string) (bool, error)
		})
		if !ok {
			return false, errors.New("atomic final fixture backend not available")
		}
		return writer.RestoreAcceptedFinalForSession(ctx, expected, message, final)
	})
}

func (s *retryFinalStore) restoreFinalAttempt(ctx context.Context, message string, save func() (bool, error)) (bool, error) {
	if message != "queue-A" {
		return save()
	}
	s.mu.Lock()
	s.attempts++
	number := s.attempts
	s.mu.Unlock()
	at := time.Now()
	failures := 2
	if s.ambiguous {
		failures = 1
	}
	var err error
	saved := false
	if number > failures || s.ambiguous {
		saved, err = save()
		if !saved && err == nil {
			return false, nil
		}
	}
	if err == nil && number <= failures {
		err = errors.New("SYNTHETIC_FINAL_WRITE_ERROR_SENTINEL")
	}
	// Observation must not introduce retry deadlocks if a regression loops.
	select {
	case s.observed <- finalSaveAttempt{number, at, err}:
	default:
	}
	if err != nil {
		return saved, err
	}
	// The final bytes have reached physical storage, but commit/finalization
	// cannot proceed until the test has reread main/steer/queue and history.
	select {
	case <-s.releaseSaved:
		return saved, nil
	case <-ctx.Done():
		return saved, ctx.Err()
	}
}

func TestFinalSaveRetryPreservesActualRuntimeMainSteerAndQueuedSuccessor(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		name := "two-transient-failures"
		if ambiguous {
			name = "committed-write-returned-error"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			base, sessions := acceptedProofFixture(t)
			id := sessions[0].ID()
			store := &retryFinalStore{SessionStore: base, ambiguous: ambiguous, observed: make(chan finalSaveAttempt, 8), releaseSaved: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(store.releaseSaved) }) }
			defer release()
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestFinalSaveRetryAdapterProcess$"}, Env: []string{"BRIA_FINAL_SAVE_RETRY_HELPER=1"}},
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
			lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "retry-test", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			custody := durablecomposition.InputCustody{Flow: flow, Wake: make(chan domain.SessionID, 8)}
			notices := make(chan telegramcontroller.Notification, 64)
			controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, runtime,
				archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error { notices <- n; return nil }),
				telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle, DurableInput: custody})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = controller.Close(context.Background()) }()
			dispatchErrors := make(chan error, 8)
			dispatcher := durablecomposition.InputDispatcher{Flow: flow, Processor: durablecomposition.NewControllerInputProcessor(controller, custody.WakeSession), Sessions: store, Wake: custody.Wake, Report: func(err error) { dispatchErrors <- err }}
			dispatchCtx, stopDispatcher := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- dispatcher.Run(dispatchCtx) }()
			defer func() { stopDispatcher(); <-done }()
			enqueue := func(message string) {
				t.Helper()
				if _, err := custody.Accept(ctx, telegramcontroller.SessionInput{SessionID: id, MessageID: message, Payload: []byte("synthetic " + message)}); err != nil {
					t.Fatal(err)
				}
			}
			enqueue("queue-A")
			waitQueueTerminalPhases(t, ctx, journalPath, id, []string{"accepted"})
			enqueue("queue-steer")
			waitQueueTerminalPhases(t, ctx, journalPath, id, []string{"accepted", "accepted"})
			_ = starter.StopCurrent(ctx, id)
			first := awaitFinalSaveAttempt(t, ctx, store.observed, 1)
			if first.err == nil {
				t.Fatal("fixture did not fail the first final save")
			}
			enqueue("queue-B")
			assertRetryFinalBody(t, ctx, sessions[0].Workdir(), id, ambiguous, false)
			wantAttempts := 3
			if ambiguous {
				wantAttempts = 2
			}
			previous := first
			for attempt := 2; attempt <= wantAttempts; attempt++ {
				observed := awaitFinalSaveAttempt(t, ctx, store.observed, attempt)
				wantDelay := time.Second
				if attempt == 3 {
					wantDelay = 2 * time.Second
				}
				if observed.at.Sub(previous.at) < wantDelay-100*time.Millisecond {
					t.Errorf("retry %d ran too early: %s want >=%s", attempt, observed.at.Sub(previous.at), wantDelay)
				}
				previous = observed
				if (observed.err == nil) != (attempt == wantAttempts) {
					t.Fatalf("attempt %d error=%v", attempt, observed.err)
				}
				inputs := terminalJournalInputs(t, ctx, journalPath, id)
				if len(inputs) != 3 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputAccepted || inputs[2].Phase != messagejournal.InputPending {
					t.Fatalf("retry %d released unresolved main/steer/queued B: %+v", attempt, inputs)
				}
				current, err := store.Load(ctx, id)
				if err != nil || current.Status() != domain.SessionRunning {
					t.Fatalf("retry %d prematurely finished lifecycle: %s err=%v", attempt, current.Status(), err)
				}
				assertRetryFinalBody(t, ctx, sessions[0].Workdir(), id, ambiguous || attempt == wantAttempts, false)
			}
			var submitted []string
			for len(runtime.submitted) != 0 {
				submitted = append(submitted, <-runtime.submitted)
			}
			if !reflect.DeepEqual(submitted, []string{"queue-A", "queue-steer"}) {
				t.Fatalf("retry sent new provider input/replay: %v", submitted)
			}
			var delivered []telegramcontroller.Notification
			for len(notices) != 0 {
				n := <-notices
				if n.Kind == telegramcontroller.NotificationFinal {
					t.Fatalf("final was delivered before final-save returned: %+v", n)
				}
				delivered = append(delivered, n)
			}
			// Only release the successful idempotent final write. No new incoming
			// message or wake is sent; the existing completion pipeline admits B.
			release()
			waitQueueTerminalPhases(t, ctx, journalPath, id, []string{"completed", "completed", "completed"})
			if err := controller.Close(ctx); err != nil {
				t.Fatal(err)
			}
			for len(runtime.submitted) != 0 {
				submitted = append(submitted, <-runtime.submitted)
			}
			if !reflect.DeepEqual(submitted, []string{"queue-A", "queue-steer", "queue-B"}) {
				t.Fatalf("actual provider input order=%v", submitted)
			}
			assertRetryFinalBody(t, ctx, sessions[0].Workdir(), id, true, true)
			finals := map[string]int{}
			for len(notices) != 0 {
				delivered = append(delivered, <-notices)
			}
			warnings, recovered := 0, 0
			for _, n := range delivered {
				if strings.Contains(n.Text, "SYNTHETIC_FINAL_WRITE_ERROR_SENTINEL") {
					t.Error("raw storage fault leaked to user notice")
				}
				if n.Kind == telegramcontroller.NotificationFinal {
					finals[n.Text]++
				}
				if n.OperationID == "queue-A:final-history-error" {
					warnings++
					if !strings.Contains(n.Text, "автоматически") || !strings.Contains(n.Text, "не повтор") {
						t.Errorf("first warning does not explain safe automatic retry: %q", n.Text)
					}
				}
				if n.OperationID == "queue-A:final-history-restored" {
					recovered++
				}
			}
			if warnings != 1 || recovered != 1 {
				t.Errorf("retry notices warning=%d restored=%d want=1/1", warnings, recovered)
			}
			if !reflect.DeepEqual(finals, map[string]int{"RETRY_A_FINAL": 1, "RETRY_B_FINAL": 1}) {
				t.Fatalf("duplicate/missing final notifications=%v", finals)
			}
			for len(dispatchErrors) != 0 {
				t.Errorf("dispatch: %v", <-dispatchErrors)
			}
		})
	}
}

func awaitFinalSaveAttempt(t *testing.T, ctx context.Context, attempts <-chan finalSaveAttempt, number int) finalSaveAttempt {
	t.Helper()
	select {
	case got := <-attempts:
		if got.number != number {
			t.Fatalf("final attempt number=%d want=%d", got.number, number)
		}
		return got
	case <-time.After(3 * time.Second):
		t.Fatalf("received final was not retried: missing attempt %d", number)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return finalSaveAttempt{}
}

func assertRetryFinalBody(t *testing.T, ctx context.Context, dir string, id domain.SessionID, wantA, wantB bool) {
	t.Helper()
	reopened, err := storage.OpenSessionStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	finals := map[string]int{}
	for _, block := range blocks {
		if block.Kind == "final" {
			finals[block.Text]++
		}
	}
	want := map[string]int{}
	if wantA {
		want["RETRY_A_FINAL"] = 1
	}
	if wantB {
		want["RETRY_B_FINAL"] = 1
	}
	if !reflect.DeepEqual(finals, want) {
		t.Fatalf("physical final bodies=%v want=%v", finals, want)
	}
}

func TestFinalSaveRetryAdapterProcess(t *testing.T) {
	if os.Getenv("BRIA_FINAL_SAVE_RETRY_HELPER") == "" {
		return
	}
	emit := func(value any) {
		if json.NewEncoder(os.Stdout).Encode(value) != nil {
			os.Exit(81)
		}
	}
	emit(map[string]any{"protocol": 1, "type": "ready", "provider_session_id": "provider-" + os.Getenv("BRIA_SESSION_ID"), "readiness": "protocol", "authentication": "unknown"})
	scanner := bufio.NewScanner(os.Stdin)
	root := ""
	seen := map[string]bool{}
	for scanner.Scan() {
		var m struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			MessageID string `json:"message_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			os.Exit(82)
		}
		switch m.Type {
		case "submit", "steer":
			if seen[m.MessageID] {
				os.Exit(83)
			}
			seen[m.MessageID] = true
			emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": m.RequestID, "message_id": m.MessageID})
			switch m.MessageID {
			case "queue-A":
				root = m.RequestID
			case "queue-steer":
				if m.Type != "steer" {
					os.Exit(84)
				}
			case "queue-B":
				if m.Type != "submit" {
					os.Exit(85)
				}
				emit(map[string]any{"protocol": 1, "type": "final", "request_id": m.RequestID, "text": "RETRY_B_FINAL"})
				emit(map[string]any{"protocol": 1, "type": "completed", "request_id": m.RequestID, "status": "completed"})
			default:
				os.Exit(86)
			}
		case "interrupt":
			emit(map[string]any{"protocol": 1, "type": "final", "request_id": root, "text": "RETRY_A_FINAL"})
			emit(map[string]any{"protocol": 1, "type": "completed", "request_id": root, "status": "completed"})
		case "close":
			os.Exit(0)
		default:
			os.Exit(87)
		}
	}
	os.Exit(0)
}
