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
	"bria/internal/turnprocessing"
)

type queueTerminalRuntime struct {
	*sessionruntime.Starter
	stopping      chan struct{}
	release       chan struct{}
	submitted     chan string
	attachmentDir string
}

func (r queueTerminalRuntime) SubmitPreparedWithCallbacks(ctx context.Context, id domain.SessionID, input turnprocessing.PreparedInput, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	r.submitted <- cb.MessageID
	structured := sessionruntime.StructuredInput{Text: input.Text}
	for _, ref := range input.Attachments {
		structured.Attachments = append(structured.Attachments, sessionruntime.LocalAttachment{Path: filepath.Join(r.attachmentDir, ref.Reference), Size: ref.Size, SHA256: ref.SHA256})
	}
	return r.Starter.SubmitStructuredWithCallbacks(ctx, id, structured, cb)
}

type queueTerminalAttachments struct {
	entered chan turnprocessing.AttachmentReceipt
	release chan struct{}
}

func (queueTerminalAttachments) MarkAccepted(context.Context, turnprocessing.AttachmentReceipt) error {
	return nil
}
func (a queueTerminalAttachments) MarkCompleted(ctx context.Context, receipt turnprocessing.AttachmentReceipt) error {
	a.entered <- receipt
	select {
	case <-a.release:
		return errors.New("synthetic attachment completion storage failure")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r queueTerminalRuntime) StopCurrent(ctx context.Context, id domain.SessionID) error {
	close(r.stopping)
	select {
	case <-r.release:
		return r.Starter.StopCurrent(ctx, id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r queueTerminalRuntime) SubmitWithCallbacks(ctx context.Context, id domain.SessionID, text string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	r.submitted <- cb.MessageID
	return r.Starter.SubmitWithCallbacks(ctx, id, text, cb)
}

func (r queueTerminalRuntime) SubmitCurrentWithCallbacks(ctx context.Context, id domain.SessionID, input sessionruntime.StructuredInput, cb sessionruntime.TurnCallbacks) error {
	r.submitted <- cb.MessageID
	return r.Starter.SubmitCurrentWithCallbacks(ctx, id, input, cb)
}

// Observe actual dispatch reads, without replacing persisted lifecycle state.
type queueTerminalSessions struct {
	*storage.SessionStore
	loaded chan domain.SessionStatus
}

func (s queueTerminalSessions) Load(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	value, err := s.SessionStore.Load(ctx, id)
	select {
	case s.loaded <- value.Status():
	default:
	}
	return value, err
}

func TestTerminalQueueContinuationAfterActualProviderProof(t *testing.T) {
	for _, mode := range []string{"interrupted", "failed", "eof", "attachment-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			store, sessions := acceptedProofFixture(t)
			id := sessions[0].ID()
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestTerminalQueueAdapterProcess$"}, Env: []string{"BRIA_TERMINAL_QUEUE_HELPER=" + mode}},
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
			runtime := queueTerminalRuntime{Starter: starter, stopping: make(chan struct{}), release: make(chan struct{}), submitted: make(chan string, 16), attachmentDir: sessions[0].Workdir()}
			attachments := queueTerminalAttachments{entered: make(chan turnprocessing.AttachmentReceipt, 1), release: make(chan struct{})}
			var attachmentsOnce sync.Once
			releaseAttachments := func() { attachmentsOnce.Do(func() { close(attachments.release) }) }
			var once sync.Once
			release := func() { once.Do(func() { close(runtime.release) }) }
			defer release()
			lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "queue-test", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			custody := durablecomposition.InputCustody{Flow: flow, Wake: make(chan domain.SessionID, 8)}
			notices := make(chan telegramcontroller.Notification, 32)
			controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, runtime,
				archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error { notices <- n; return nil }),
				telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle, Stopper: runtime, DurableInput: custody, Attachments: attachments})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); releaseAttachments(); _ = controller.Close(context.Background()) }()
			observed := queueTerminalSessions{SessionStore: store, loaded: make(chan domain.SessionStatus, 32)}
			dispatchErrors := make(chan error, 16)
			dispatcher := durablecomposition.InputDispatcher{Flow: flow, Processor: durablecomposition.NewControllerInputProcessor(controller, custody.WakeSession), Sessions: observed, Wake: custody.Wake, Report: func(err error) { dispatchErrors <- err }}
			dispatchCtx, stopDispatcher := context.WithCancel(ctx)
			dispatchDone := make(chan error, 1)
			go func() { dispatchDone <- dispatcher.Run(dispatchCtx) }()
			defer func() { stopDispatcher(); <-dispatchDone }()
			enqueue := func(message string) {
				t.Helper()
				input := telegramcontroller.SessionInput{SessionID: id, MessageID: message, Payload: []byte("synthetic " + message)}
				if mode == "attachment-failure" && message == "queue-A" {
					input.Attachments = []telegramcontroller.AttachmentRef{{Reference: "synthetic-photo", Size: 3, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}
				}
				if _, err := custody.Accept(ctx, input); err != nil {
					t.Fatal(err)
				}
			}
			enqueue("queue-A")
			waitQueueTerminalPhases(t, ctx, journalPath, id, []string{"accepted"})
			enqueue("queue-steer")
			waitQueueTerminalPhases(t, ctx, journalPath, id, []string{"accepted", "accepted"})
			if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticStop, SessionID: id}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-runtime.stopping:
			case <-ctx.Done():
				t.Fatal("Stop did not reach runtime")
			}
			waitStoppingRead := func() {
				t.Helper()
				for {
					select {
					case status := <-observed.loaded:
						if status == domain.SessionStopping {
							return
						}
					case <-ctx.Done():
						t.Fatal("dispatcher did not consume stopping wake")
					}
				}
			}
			// End the previous Running drain before B is enqueued. Then consume
			// B's incoming wake while stopping, so it cannot mask a missing
			// post-terminal wake. No new incoming wake is sent after release.
			custody.WakeSession(id)
			waitStoppingRead()
			enqueue("queue-B")
			waitStoppingRead()
			waitQueueTerminalPhases(t, ctx, journalPath, id, []string{"accepted", "accepted", "pending"})
			release()
			if mode == "attachment-failure" {
				select {
				case receipt := <-attachments.entered:
					if receipt.MessageID != "queue-A" || receipt.Reference != "synthetic-photo" || receipt.ProviderSession != binding.SessionID {
						t.Fatalf("attachment completion identity=%+v", receipt)
					}
				case <-ctx.Done():
					t.Fatal("attachment completion not reached")
				}
				waitQueueTerminalPhases(t, ctx, journalPath, id, []string{"accepted", "accepted", "pending"})
				releaseAttachments()
			}
			wantPhases := []string{"terminal_failed", "terminal_failed", "completed"}
			if mode == "eof" || mode == "attachment-failure" {
				wantPhases = []string{"unknown", "unknown", "pending"}
			}
			waitQueueTerminalPhases(t, ctx, journalPath, id, wantPhases)
			if err := controller.Close(ctx); err != nil {
				t.Fatal(err)
			}
			var submitted []string
			for len(runtime.submitted) != 0 {
				submitted = append(submitted, <-runtime.submitted)
			}
			wantSubmitted := []string{"queue-A", "queue-steer", "queue-B"}
			if mode == "eof" || mode == "attachment-failure" {
				wantSubmitted = wantSubmitted[:2]
			}
			if !reflect.DeepEqual(submitted, wantSubmitted) {
				t.Errorf("actual runtime input order=%v want=%v (no replay)", submitted, wantSubmitted)
			}
			reopened, err := storage.OpenSessionStore(filepath.Join(sessions[0].Workdir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			history, err := reopened.LoadCardHistory(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var terminalText string
			finals := 0
			for len(notices) != 0 {
				n := <-notices
				if n.OperationID == "queue-A:error" {
					terminalText = n.Text
				}
				if n.Kind == telegramcontroller.NotificationFinal {
					finals++
					if n.Text != "QUEUE_B_FINAL" {
						t.Errorf("unexpected final=%q", n.Text)
					}
				}
			}
			if terminalText == "" || !strings.Contains(strings.Join(history, "\n"), terminalText) {
				t.Errorf("terminal UI not physically retained: notice=%q history=%v", terminalText, history)
			}
			if mode == "interrupted" && !strings.Contains(strings.ToLower(terminalText), "останов") {
				t.Errorf("confirmed Stop copy=%q, want explicit stopped outcome", terminalText)
			}
			wantFinals := 1
			if mode == "eof" || mode == "attachment-failure" {
				wantFinals = 0
			}
			if finals != wantFinals {
				t.Errorf("final notifications=%d want=%d", finals, wantFinals)
			}
			if got := strings.Count(strings.Join(history, "\n"), "QUEUE_B_FINAL"); got != wantFinals {
				t.Errorf("physically stored B final count=%d want=%d", got, wantFinals)
			}
			for len(dispatchErrors) != 0 {
				t.Errorf("dispatcher: %v", <-dispatchErrors)
			}
		})
	}
}

func waitQueueTerminalPhases(t *testing.T, ctx context.Context, path string, id domain.SessionID, want []string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		inputs := terminalJournalInputs(t, ctx, path, id)
		var phases []string
		for i, input := range inputs {
			identities := []string{"queue-A", "queue-steer", "queue-B"}
			if i >= len(identities) || input.MessageID != identities[i] {
				t.Fatalf("unexpected physical journal identity=%s at index=%d", input.MessageID, i)
			}
			if input.Sequence != uint64(i+1) {
				t.Fatalf("unordered journal sequence=%d at index=%d", input.Sequence, i)
			}
			phases = append(phases, string(input.Phase))
		}
		if reflect.DeepEqual(phases, want) {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("reopened journal phases=%v want=%v", phases, want)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestTerminalQueueAdapterProcess(t *testing.T) {
	mode := os.Getenv("BRIA_TERMINAL_QUEUE_HELPER")
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
	seen := make(map[string]bool)
	root := ""
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
		case "submit", "steer":
			if seen[message.MessageID] {
				os.Exit(84)
			}
			seen[message.MessageID] = true
			emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
			switch message.MessageID {
			case "queue-A":
				root = message.RequestID
			case "queue-steer":
				if message.Type != "steer" {
					os.Exit(85)
				}
			case "queue-B":
				if message.Type != "submit" {
					os.Exit(86)
				}
				emit(map[string]any{"protocol": 1, "type": "final", "request_id": message.RequestID, "text": "QUEUE_B_FINAL"})
				emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "completed"})
			default:
				os.Exit(87)
			}
		case "interrupt":
			if mode == "eof" {
				os.Exit(1)
			}
			status, code := mode, mode
			if mode == "failed" {
				code = sessionruntime.ErrorProvider
			}
			if mode == "attachment-failure" {
				status, code = "interrupted", "interrupted"
			}
			emit(map[string]any{"protocol": 1, "type": "completed", "request_id": root, "status": status, "error_code": code})
		case "close":
			os.Exit(0)
		default:
			os.Exit(83)
		}
	}
	os.Exit(0)
}
