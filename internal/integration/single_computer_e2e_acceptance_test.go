package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionid"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
	workdirvalidator "bria/internal/workdir"
)

const syntheticAdapterEnvironment = "BRIA_ACCEPTANCE_SYNTHETIC_ADAPTER"

// TestSyntheticSingleComputerAdapterChild is a physical provider-neutral
// process boundary. It proves that the coordinator flow starts, submits to,
// closes, and exactly resumes one real child process rather than only changing
// an in-memory status.
func TestSyntheticSingleComputerAdapterChild(t *testing.T) {
	if os.Getenv(syntheticAdapterEnvironment) == "" {
		t.Skip("helper process entrypoint")
	}
	mode := app.SessionStartMode(os.Getenv(sessionruntime.EnvironmentStartMode))
	logicalID := domain.SessionID(os.Getenv("BRIA_SESSION_ID"))
	provider := domain.Provider(os.Getenv("BRIA_PROVIDER"))
	persistedStore, err := storage.OpenSessionStore(os.Getenv("BRIA_ACCEPTANCE_STORE"))
	if err != nil {
		childExit("open durable synthetic state before readiness: %v", err)
	}
	persisted, ok, err := persistedStore.GetByIntent(context.Background(), domain.IntentID(os.Getenv("BRIA_ACCEPTANCE_INTENT")))
	if err != nil || !ok {
		childExit("read durable synthetic session before readiness: found=%v err=%v", ok, err)
	}
	if persisted.ID() != logicalID || persisted.Provider() != provider {
		childExit("synthetic runtime identity differs from durable session")
	}
	providerID := "synthetic-provider-for-" + string(logicalID)
	kind := "synthetic-start"
	wantStatus := domain.SessionStarting
	if mode == app.SessionStartResume {
		providerID = os.Getenv(sessionruntime.EnvironmentProviderSession)
		kind = "synthetic-resume"
		wantStatus = domain.SessionArchived
	}
	if configured := os.Getenv("BRIA_ACCEPTANCE_EXPECTED_STATUS"); configured != "" {
		wantStatus = domain.SessionStatus(configured)
	} else if mode == app.SessionStartResume && os.Getenv("BRIA_ACCEPTANCE_CRASH_GATE") != "" {
		wantStatus = domain.SessionAwaitingRecovery
	}
	if mode != app.SessionStartNew && mode != app.SessionStartResume {
		childExit("synthetic adapter start mode %q is invalid", mode)
	}
	if persisted.Status() != wantStatus {
		childExit("synthetic durable status = %q, want %q", persisted.Status(), wantStatus)
	}
	if prior, bound := persisted.Binding(); mode == app.SessionStartResume && (!bound || prior.SessionID != providerID) {
		childExit("synthetic resume does not match durable provider binding")
	} else if mode == app.SessionStartNew && bound {
		childExit("synthetic new session is already bound before readiness")
	}
	workdir, err := os.Getwd()
	if err != nil {
		childExit("read synthetic adapter workdir: %v", err)
	}
	if workdir != persisted.Workdir() {
		childExit("synthetic process workdir = %q, durable workdir %q", workdir, persisted.Workdir())
	}
	if err := appendReceipt(os.Getenv("BRIA_ACCEPTANCE_RECEIPT"), providerChildReceipt{
		Kind: kind, Provider: provider, LogicalSessionID: logicalID,
		ProviderSessionID: providerID, StartMode: mode, Workdir: workdir, StatusBeforeReady: persisted.Status(),
	}); err != nil {
		childExit("record synthetic adapter start: %v", err)
	}
	ready := map[string]any{
		"protocol": sessionruntime.ProtocolVersion, "type": "ready",
		"provider_session_id": providerID, "readiness": sessionruntime.ReadinessProtocol,
		"authentication": sessionruntime.AuthenticationUnknown,
	}
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(ready); err != nil {
		childExit("write synthetic adapter readiness: %v", err)
	}
	if crashGate := os.Getenv("BRIA_ACCEPTANCE_CRASH_GATE"); crashGate != "" {
		go func() {
			for {
				if _, err := os.Stat(crashGate); err == nil {
					if err := os.Remove(crashGate); err != nil {
						childExit("consume synthetic crash gate: %v", err)
					}
					_ = appendReceipt(os.Getenv("BRIA_ACCEPTANCE_RECEIPT"), providerChildReceipt{
						Kind: "synthetic-crash", Provider: provider, LogicalSessionID: logicalID,
						ProviderSessionID: providerID, StartMode: mode, Workdir: workdir,
					})
					os.Exit(0)
				} else if !errors.Is(err, os.ErrNotExist) {
					childExit("read synthetic crash gate: %v", err)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			Protocol  int    `json:"protocol"`
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			MessageID string `json:"message_id"`
			Text      string `json:"text"`
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Protocol != sessionruntime.ProtocolVersion {
			childExit("decode synthetic adapter request: %v", err)
		}
		switch request.Type {
		case "submit":
			if request.RequestID == "" || request.MessageID == "" || request.Text == "" {
				childExit("synthetic submit is incomplete")
			}
			if err := appendReceipt(os.Getenv("BRIA_ACCEPTANCE_RECEIPT"), providerChildReceipt{
				Kind: "synthetic-submit", Provider: provider, LogicalSessionID: logicalID,
				ProviderSessionID: providerID, StartMode: mode, Workdir: workdir,
			}); err != nil {
				childExit("record synthetic submit: %v", err)
			}
			responses := []map[string]any{
				{"protocol": sessionruntime.ProtocolVersion, "type": "accepted", "request_id": request.RequestID, "message_id": request.MessageID},
				{"protocol": sessionruntime.ProtocolVersion, "type": "event", "request_id": request.RequestID, "kind": sessionruntime.EventCommentary, "text": "working: " + request.Text},
			}
			for _, response := range responses {
				if err := encoder.Encode(response); err != nil {
					childExit("write synthetic adapter response: %v", err)
				}
			}
			if request.Text == "slow request" {
				gate := os.Getenv("BRIA_ACCEPTANCE_GATE")
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(gate); err == nil {
						break
					} else if !errors.Is(err, os.ErrNotExist) {
						childExit("read synthetic completion gate: %v", err)
					}
					if time.Now().After(deadline) {
						childExit("synthetic completion gate timed out")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			for _, response := range []map[string]any{
				{"protocol": sessionruntime.ProtocolVersion, "type": "final", "request_id": request.RequestID, "text": "answer: " + request.Text},
				{"protocol": sessionruntime.ProtocolVersion, "type": "completed", "request_id": request.RequestID, "status": sessionruntime.StatusCompleted},
			} {
				if err := encoder.Encode(response); err != nil {
					childExit("write synthetic adapter terminal: %v", err)
				}
			}
		case "close":
			if err := appendReceipt(os.Getenv("BRIA_ACCEPTANCE_RECEIPT"), providerChildReceipt{
				Kind: "synthetic-close", Provider: provider, LogicalSessionID: logicalID,
				ProviderSessionID: providerID, StartMode: mode, Workdir: workdir,
			}); err != nil {
				childExit("record synthetic adapter close: %v", err)
			}
			return
		default:
			childExit("unsupported synthetic adapter request %q", request.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		childExit("scan synthetic adapter requests: %v", err)
	}
	childExit("synthetic adapter stdin closed without close")
}

func TestSingleComputerSyntheticTelegramCreateSubmitCloseAndExactResume(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			const ownerID, chatID = int64(7001), int64(8002)
			const firstUpdateID = int64(100)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			root := t.TempDir()
			workdir := filepath.Join(root, "workdir")
			if err := os.Mkdir(workdir, 0o700); err != nil {
				t.Fatalf("create workdir: %v", err)
			}
			workdir, err = filepath.EvalSymlinks(workdir)
			if err != nil {
				t.Fatalf("resolve physical workdir: %v", err)
			}
			statePath := filepath.Join(root, "state.json")
			receiptPath := filepath.Join(root, "provider.jsonl")
			store, err := storage.OpenSessionStore(statePath)
			if err != nil {
				t.Fatalf("open unified state: %v", err)
			}
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				provider: {
					Path: testBinary, Args: []string{"-test.run=^TestSyntheticSingleComputerAdapterChild$"},
					Env: []string{
						syntheticAdapterEnvironment + "=1", "BRIA_ACCEPTANCE_RECEIPT=" + receiptPath,
						"BRIA_ACCEPTANCE_STORE=" + statePath,
						"BRIA_ACCEPTANCE_INTENT=telegram-update:100",
					},
				},
			}, sessionruntime.Options{HandshakeTimeout: 2 * time.Second})
			if err != nil {
				t.Fatalf("create physical provider starter: %v", err)
			}
			ids, err := sessionid.NewWithReader(bytes.NewReader(bytes.Repeat([]byte{0x21}, 64)))
			if err != nil {
				t.Fatalf("create deterministic session ids: %v", err)
			}
			creator, err := app.NewSessionCreator("local", workdirvalidator.ExistingDirectory{}, ids, store, starter)
			if err != nil {
				t.Fatalf("create session application: %v", err)
			}
			turns, err := app.NewSessionTurnLifecycle(store, func() time.Time { return time.Now().UTC() })
			if err != nil {
				t.Fatalf("create durable turn lifecycle: %v", err)
			}
			closer, err := app.NewSessionCloser(store, starter, func() time.Time { return time.Now().UTC() })
			if err != nil {
				t.Fatalf("create confirmed session closer: %v", err)
			}
			resumer, err := app.NewArchivedSessionResumer(store, starter, domain.SessionLifetime6Hours, func() time.Time { return time.Now().UTC() })
			if err != nil {
				t.Fatalf("create exact archive resumer: %v", err)
			}
			notifier := newSyntheticNotifier()
			controller, err := telegramcontroller.New(ownerID, chatID, "local", creator, store, starter, notifier, telegramcontroller.Options{
				Lifecycle: starter, Stopper: starter, ArchivedResumer: resumer,
				SessionCloser: closer, TurnLifecycle: turns,
			})
			if err != nil {
				t.Fatalf("compose Telegram controller: %v", err)
			}
			defer func() {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer closeCancel()
				if err := controller.Close(closeCtx); err != nil {
					t.Errorf("close controller: %v", err)
				}
			}()

			source := newSyntheticUpdateSource(firstUpdateID)
			sender := newSyntheticStatusSender()
			loop, err := coordinator.NewLoop(source, store.CoordinatorCheckpoints(), controller, sender, syntheticReadiness{})
			if err != nil {
				t.Fatalf("compose coordinator loop: %v", err)
			}
			loopDone := make(chan error, 1)
			go func() { loopDone <- loop.Run(ctx) }()

			createText := fmt.Sprintf("/new %s %s", provider, workdir)
			source.offer(t, coordinator.Update{
				ID: firstUpdateID, Kind: coordinator.UpdateMessage, ActorID: ownerID,
				ConversationID: chatID, ConversationKind: "private", Text: createText,
			})
			sender.wait(t, "status:100")
			sessions, err := store.List(context.Background())
			if err != nil || len(sessions) != 1 || sessions[0].Status() != domain.SessionReady {
				t.Fatalf("durable ready session = (%#v, %v), want exactly one", sessions, err)
			}
			sessionID := sessions[0].ID()
			originalBinding, ok := sessions[0].Binding()
			if !ok {
				t.Fatal("ready session has no provider binding")
			}
			startReceipt := waitForReceipt(t, receiptPath, "synthetic-start", 2*time.Second)
			if startReceipt.ProviderSessionID != originalBinding.SessionID || startReceipt.Provider != provider {
				t.Fatalf("physical start receipt = %#v, binding %#v", startReceipt, originalBinding)
			}

			source.offer(t, coordinator.Update{
				ID: firstUpdateID + 1, Kind: coordinator.UpdateMessage, ActorID: ownerID,
				ConversationID: chatID, ConversationKind: "private", Text: "first request",
			})
			sender.wait(t, "status:101")
			notifier.waitFinal(t, sessionID, "answer: first request")
			waitForReceipt(t, receiptPath, "synthetic-submit", 2*time.Second)

			closed, err := controller.CloseSession(context.Background(), sessionID)
			if err != nil || closed.Kind != coordinator.DecisionStatus {
				t.Fatalf("semantic close = (%#v, %v)", closed, err)
			}
			waitForReceipt(t, receiptPath, "synthetic-close", 2*time.Second)
			archived := loadSession(t, statePath, sessionID)
			if archived.Status() != domain.SessionArchived {
				t.Fatalf("durable state after confirmed close = %q, want archived", archived.Status())
			}

			if _, err := controller.ResumeArchived(context.Background(), sessionID); err != nil {
				t.Fatalf("semantic exact resume: %v", err)
			}
			resumeReceipt := waitForReceipt(t, receiptPath, "synthetic-resume", 2*time.Second)
			resumed := loadSession(t, statePath, sessionID)
			resumedBinding, ok := resumed.Binding()
			if !ok || resumed.Status() != domain.SessionReady || resumedBinding.SessionID != originalBinding.SessionID ||
				resumedBinding.Generation != originalBinding.Generation+1 || resumeReceipt.ProviderSessionID != originalBinding.SessionID {
				t.Fatalf("exact resumed state/receipt = (%#v, %#v), original %#v", resumed.Snapshot(), resumeReceipt, originalBinding)
			}

			source.offer(t, coordinator.Update{
				ID: firstUpdateID + 2, Kind: coordinator.UpdateMessage, ActorID: ownerID,
				ConversationID: chatID, ConversationKind: "private", Text: "after resume",
			})
			sender.wait(t, "status:102")
			notifier.waitFinal(t, sessionID, "answer: after resume")

			if _, err := controller.CloseSession(context.Background(), sessionID); err != nil {
				t.Fatalf("close resumed session: %v", err)
			}
			if got := countReceipts(t, receiptPath, "synthetic-start"); got != 1 {
				t.Fatalf("fresh provider starts = %d, want 1", got)
			}
			if got := countReceipts(t, receiptPath, "synthetic-resume"); got != 1 {
				t.Fatalf("exact provider resumes = %d, want 1", got)
			}
			if got := countReceipts(t, receiptPath, "synthetic-submit"); got != 2 {
				t.Fatalf("provider submits = %d, want 2", got)
			}
			if got := countReceipts(t, receiptPath, "synthetic-close"); got != 2 {
				t.Fatalf("confirmed provider closes = %d, want 2", got)
			}

			checkpointStore, err := storage.OpenCoordinatorCheckpointStore(statePath)
			if err != nil {
				t.Fatalf("reopen durable checkpoint store: %v", err)
			}
			checkpoint, found, err := checkpointStore.Load(context.Background())
			if err != nil || !found || checkpoint.Checkpoint.NextUpdateID != 103 ||
				checkpoint.Checkpoint.Outbound == nil || checkpoint.Checkpoint.Outbound.Phase != coordinator.OutboundConfirmed {
				t.Fatalf("reopened coordinator checkpoint = (%#v, %v, %v)", checkpoint, found, err)
			}
			if final := loadSession(t, statePath, sessionID); final.Status() != domain.SessionArchived {
				t.Fatalf("final durable session = %q, want archived", final.Status())
			}

			cancel()
			select {
			case err := <-loopDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("coordinator loop stop = %v, want context canceled", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("coordinator loop did not stop")
			}
		})
	}
}

func TestBusySessionCloseWaitsForAcceptedWorkThenPhysicallyArchives(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}
	const ownerID, chatID = int64(7001), int64(8002)
	root := t.TempDir()
	workdir := filepath.Join(root, "workdir")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	workdir, err = filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatalf("resolve physical workdir: %v", err)
	}
	statePath := filepath.Join(root, "state.json")
	receiptPath := filepath.Join(root, "provider.jsonl")
	gatePath := filepath.Join(root, "finish-turn")
	store, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatalf("open unified state: %v", err)
	}
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
		domain.ProviderCodex: {
			Path: testBinary, Args: []string{"-test.run=^TestSyntheticSingleComputerAdapterChild$"},
			Env: []string{
				syntheticAdapterEnvironment + "=1", "BRIA_ACCEPTANCE_RECEIPT=" + receiptPath,
				"BRIA_ACCEPTANCE_STORE=" + statePath, "BRIA_ACCEPTANCE_INTENT=telegram-update:200",
				"BRIA_ACCEPTANCE_GATE=" + gatePath,
			},
		},
	}, sessionruntime.Options{HandshakeTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("create physical provider starter: %v", err)
	}
	ids, err := sessionid.NewWithReader(bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)))
	if err != nil {
		t.Fatalf("create deterministic session ids: %v", err)
	}
	creator, err := app.NewSessionCreator("local", workdirvalidator.ExistingDirectory{}, ids, store, starter)
	if err != nil {
		t.Fatalf("create session application: %v", err)
	}
	turns, err := app.NewSessionTurnLifecycle(store, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatalf("create durable turn lifecycle: %v", err)
	}
	closer, err := app.NewSessionCloser(store, starter, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatalf("create confirmed session closer: %v", err)
	}
	notifier := newSyntheticNotifier()
	controller, err := telegramcontroller.New(ownerID, chatID, "local", creator, store, starter, notifier, telegramcontroller.Options{
		Lifecycle: starter, Stopper: starter, SessionCloser: closer, TurnLifecycle: turns,
	})
	if err != nil {
		t.Fatalf("compose Telegram controller: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := controller.Close(closeCtx); err != nil {
			t.Errorf("close controller: %v", err)
		}
	}()

	ownerUpdate := func(id int64, text string) coordinator.Update {
		return coordinator.Update{ID: id, Kind: coordinator.UpdateMessage, ActorID: ownerID, ConversationID: chatID, ConversationKind: "private", Text: text}
	}
	if _, err := controller.Handle(context.Background(), ownerUpdate(200, fmt.Sprintf("/new codex %s", workdir))); err != nil {
		t.Fatalf("create ready session: %v", err)
	}
	sessions, err := store.List(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("created sessions = (%#v, %v)", sessions, err)
	}
	sessionID := sessions[0].ID()
	if _, err := controller.Handle(context.Background(), ownerUpdate(201, "slow request")); err != nil {
		t.Fatalf("submit slow request: %v", err)
	}
	waitForReceipt(t, receiptPath, "synthetic-submit", 2*time.Second)
	waitForStatus(t, statePath, sessionID, domain.SessionRunning, 2*time.Second)

	decision, err := controller.CloseSession(context.Background(), sessionID)
	if err != nil || decision.Kind != coordinator.DecisionStatus {
		t.Fatalf("schedule busy close = (%#v, %v)", decision, err)
	}
	if current := loadSession(t, statePath, sessionID); current.Status() != domain.SessionClosingAfterWork {
		t.Fatalf("durable busy-close state = %q, want closing_after_work", current.Status())
	}
	if got := countReceipts(t, receiptPath, "synthetic-close"); got != 0 {
		t.Fatalf("provider closed before accepted work completed: %d receipts", got)
	}

	if err := os.WriteFile(gatePath, []byte("finish"), 0o600); err != nil {
		t.Fatalf("release provider completion gate: %v", err)
	}
	notifier.waitFinal(t, sessionID, "answer: slow request")
	waitForReceipt(t, receiptPath, "synthetic-close", 2*time.Second)
	if final := loadSession(t, statePath, sessionID); final.Status() != domain.SessionArchived {
		t.Fatalf("session after completed accepted work = %q, want archived", final.Status())
	}
}

func TestUnexpectedPhysicalExitIsSupervisedIntoExactSameSession(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			root := t.TempDir()
			workdir := filepath.Join(root, "workdir")
			if err := os.Mkdir(workdir, 0o700); err != nil {
				t.Fatalf("create workdir: %v", err)
			}
			workdir, err = filepath.EvalSymlinks(workdir)
			if err != nil {
				t.Fatalf("resolve physical workdir: %v", err)
			}
			statePath := filepath.Join(root, "state.json")
			receiptPath := filepath.Join(root, "provider.jsonl")
			crashGate := filepath.Join(root, "crash")
			intentID := domain.IntentID("supervised-exit-" + string(provider))
			store, err := storage.OpenSessionStore(statePath)
			if err != nil {
				t.Fatalf("open unified state: %v", err)
			}
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				provider: {
					Path: testBinary, Args: []string{"-test.run=^TestSyntheticSingleComputerAdapterChild$"},
					Env: []string{
						syntheticAdapterEnvironment + "=1", "BRIA_ACCEPTANCE_RECEIPT=" + receiptPath,
						"BRIA_ACCEPTANCE_STORE=" + statePath, "BRIA_ACCEPTANCE_INTENT=" + string(intentID),
						"BRIA_ACCEPTANCE_CRASH_GATE=" + crashGate,
					},
				},
			}, sessionruntime.Options{HandshakeTimeout: 2 * time.Second})
			if err != nil {
				t.Fatalf("create physical provider starter: %v", err)
			}
			ids, err := sessionid.NewWithReader(bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)))
			if err != nil {
				t.Fatalf("create deterministic session ids: %v", err)
			}
			creator, err := app.NewSessionCreator("local", workdirvalidator.ExistingDirectory{}, ids, store, starter)
			if err != nil {
				t.Fatalf("create session application: %v", err)
			}
			created, err := creator.Create(ctx, app.ConfirmedSessionIntent{
				IntentID: intentID, ComputerID: "local", Provider: provider, Workdir: workdir,
			})
			if err != nil || created.StartError != nil || created.Session.Status() != domain.SessionReady {
				t.Fatalf("create supervised session = (%#v, %v)", created, err)
			}
			prior, ok := created.Session.Binding()
			if !ok {
				t.Fatal("supervised session has no provider binding")
			}
			supervisor, err := sessionsupervisor.New(store, starter, starter, sessionsupervisor.Options{
				MaxRestartAttempts: 2,
				WaitBeforeRetry:    func(context.Context, int) error { return nil },
				Now:                func() time.Time { return time.Now().UTC() },
			})
			if err != nil {
				t.Fatalf("create process supervisor: %v", err)
			}
			if err := os.WriteFile(crashGate, []byte("crash"), 0o600); err != nil {
				t.Fatalf("trigger physical provider exit: %v", err)
			}
			waitForReceipt(t, receiptPath, "synthetic-crash", 2*time.Second)
			result, err := supervisor.Watch(ctx, created.Session.ID(), prior)
			if err != nil {
				t.Fatalf("supervise physical provider exit: %v", err)
			}
			if !result.Recovered || result.AwaitingRecovery || result.Stale || result.RestartAttempts != 1 {
				t.Fatalf("supervision result = %#v, want one exact recovery", result)
			}
			binding, ok := result.Session.Binding()
			if !ok || binding.Provider != prior.Provider || binding.SessionID != prior.SessionID || binding.Generation != prior.Generation+1 {
				t.Fatalf("supervised binding = %#v, want exact next generation from %#v", binding, prior)
			}
			resumeReceipt := waitForReceipt(t, receiptPath, "synthetic-resume", 2*time.Second)
			if resumeReceipt.ProviderSessionID != prior.SessionID || resumeReceipt.StartMode != app.SessionStartResume {
				t.Fatalf("physical supervised resume = %#v, want provider session %q", resumeReceipt, prior.SessionID)
			}
			persisted := loadSession(t, statePath, created.Session.ID())
			if !persisted.Equal(result.Session) || persisted.Status() != domain.SessionReady {
				t.Fatalf("durable supervised recovery = %#v, result %#v", persisted.Snapshot(), result.Session.Snapshot())
			}
			request := app.StartSessionRequest{
				SessionID: persisted.ID(), ComputerID: persisted.ComputerID(), Provider: persisted.Provider(),
				Workdir: persisted.Workdir(), Mode: app.SessionStartResume, PriorBinding: &prior,
			}
			if err := starter.Abort(ctx, request, binding); err != nil {
				t.Fatalf("close supervised provider: %v", err)
			}
		})
	}
}

func waitForStatus(t *testing.T, path string, id domain.SessionID, status domain.SessionStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if current := loadSession(t, path, id); current.Status() == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %q did not reach %q within %s", id, status, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func loadSession(t *testing.T, path string, id domain.SessionID) domain.Session {
	t.Helper()
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	session, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load session %q: %v", id, err)
	}
	return session
}

type syntheticUpdateSource struct {
	bootstrap int64
	updates   chan coordinator.Update
}

func newSyntheticUpdateSource(bootstrap int64) *syntheticUpdateSource {
	return &syntheticUpdateSource{bootstrap: bootstrap, updates: make(chan coordinator.Update, 4)}
}

func (source *syntheticUpdateSource) Bootstrap(context.Context) (int64, error) {
	return source.bootstrap, nil
}

func (source *syntheticUpdateSource) Poll(ctx context.Context, _ int64) ([]coordinator.Update, error) {
	select {
	case update := <-source.updates:
		return []coordinator.Update{update}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (source *syntheticUpdateSource) offer(t *testing.T, update coordinator.Update) {
	t.Helper()
	select {
	case source.updates <- update:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not accept synthetic update")
	}
}

type syntheticStatusSender struct {
	mu         sync.Mutex
	operations map[string]coordinator.Status
	notified   chan string
	nextID     int64
}

func newSyntheticStatusSender() *syntheticStatusSender {
	return &syntheticStatusSender{operations: make(map[string]coordinator.Status), notified: make(chan string, 8), nextID: 900}
}

func (sender *syntheticStatusSender) SendStatus(_ context.Context, operationID string, status coordinator.Status) (coordinator.Receipt, error) {
	sender.mu.Lock()
	sender.nextID++
	sender.operations[operationID] = status
	receipt := coordinator.Receipt{MessageID: sender.nextID}
	sender.mu.Unlock()
	sender.notified <- operationID
	return receipt, nil
}

func (sender *syntheticStatusSender) wait(t *testing.T, operationID string) coordinator.Status {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-sender.notified:
			sender.mu.Lock()
			status, ok := sender.operations[operationID]
			sender.mu.Unlock()
			if ok {
				return status
			}
		case <-deadline:
			t.Fatalf("outbound operation %q did not receive a synthetic receipt", operationID)
		}
	}
}

type syntheticReadiness struct{}

func (syntheticReadiness) Ready(context.Context, coordinator.Checkpoint) error { return nil }

type syntheticNotifier struct {
	mu      sync.Mutex
	items   []telegramcontroller.Notification
	changed chan struct{}
}

func newSyntheticNotifier() *syntheticNotifier {
	return &syntheticNotifier{changed: make(chan struct{}, 16)}
}

func (notifier *syntheticNotifier) Notify(_ context.Context, item telegramcontroller.Notification) error {
	notifier.mu.Lock()
	notifier.items = append(notifier.items, item)
	notifier.mu.Unlock()
	notifier.changed <- struct{}{}
	return nil
}

func (notifier *syntheticNotifier) waitFinal(t *testing.T, sessionID domain.SessionID, text string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		notifier.mu.Lock()
		found := false
		for _, item := range notifier.items {
			if item.SessionID == sessionID && item.Kind == telegramcontroller.NotificationFinal && item.Text == text {
				found = true
				break
			}
		}
		notifier.mu.Unlock()
		if found {
			return
		}
		select {
		case <-notifier.changed:
		case <-deadline:
			t.Fatalf("final notification %q did not appear for %q", text, sessionID)
		}
	}
}
