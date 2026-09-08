package main

import (
	"bria/internal/app"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Gate only the acceptance callback; all protocol IO, terminal results and
// cancellation draining come from the real Starter and a synthetic child.
type acceptedProofRuntime struct {
	*sessionruntime.Starter
	accepted              chan struct{}
	release               chan struct{}
	cancelAfterAcceptance bool
}

func (r acceptedProofRuntime) SubmitWithCallbacks(ctx context.Context, id domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	onAccepted := callbacks.OnAccepted
	callbacks.OnAccepted = func(messageID string) error {
		if err := onAccepted(messageID); err != nil {
			return err
		}
		close(r.accepted)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if r.cancelAfterAcceptance {
			cancel()
		}
		return nil
	}
	return r.Starter.SubmitWithCallbacks(ctx, id, text, callbacks)
}

func TestAcceptedRealStarterTerminalProofControlsLifecycle(t *testing.T) {
	for _, mode := range []string{"failed", "interrupted", "cancel", "eof"} {
		for _, closing := range []bool{false, true} {
			name := mode + "/ready"
			if closing {
				name = mode + "/close"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				store, sessions := acceptedProofFixture(t)
				id := sessions[0].ID()
				starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
					domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestAcceptedProofAdapterProcess$"}, Env: []string{"BRIA_ACCEPTED_PROOF_HELPER=" + mode}},
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
					t.Fatalf("fixture binding mismatch: %+v / %+v", binding, storedBinding)
				}
				lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				closer, err := app.NewSessionCloser(store, starter, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				runtime := acceptedProofRuntime{Starter: starter, accepted: make(chan struct{}), release: make(chan struct{}), cancelAfterAcceptance: mode == "cancel"}
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(runtime.release) }) }
				notices := make(chan telegramcontroller.Notification, 16)
				controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, runtime,
					archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error {
						if n.Kind == telegramcontroller.NotificationError {
							notices <- n
						}
						return nil
					}), telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle, SessionCloser: closer})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { release(); _ = controller.Close(context.Background()) }()
				if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: id}); err != nil {
					t.Fatal(err)
				}
				if _, err := controller.HandleSemanticMessage(ctx, coordinator.Update{ID: 802, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "synthetic terminal proof"}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-runtime.accepted:
				case <-ctx.Done():
					t.Fatal("no correlated acceptance")
				}
				if closing {
					if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: id, Choice: 1}); err != nil {
						t.Fatal(err)
					}
				} else if mode == "interrupted" || mode == "cancel" {
					if _, err := lifecycle.BeginStop(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
				release()
				if mode != "cancel" {
					// The fixture emits its chosen terminal (or EOF) only after
					// interrupt, so acceptance and scheduling are deterministic.
					_ = starter.StopCurrent(ctx, id)
				}
				select {
				case notice := <-notices:
					if (mode == "interrupted" || mode == "cancel") && notice.Text != "Запрос остановлен." {
						t.Fatalf("confirmed interruption notice=%q, want stopped rather than CLI error", notice.Text)
					}
				case <-ctx.Done():
					t.Fatal("no worker terminal notification")
				}
				current, err := store.Load(ctx, id)
				want := domain.SessionReady
				if closing {
					want = domain.SessionArchived
				}
				if mode == "eof" {
					want = domain.SessionRunning
					if closing {
						want = domain.SessionClosingAfterWork
					}
				}
				if err != nil || current.Status() != want {
					t.Fatalf("real Starter %s closing=%v: status=%s want=%s err=%v", mode, closing, current.Status(), want, err)
				}
			})
		}
	}
}

func acceptedProofFixture(t *testing.T) (*storage.SessionStore, []domain.Session) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.OpenSessionStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := domain.SessionID("11111111-1111-4111-9111-111111111111")
	starting, err := domain.NewStartingSession(id, domain.IntentID("intent-"+string(id)), "local", domain.ProviderCodex, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + string(id), Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, id, "synthetic-existing", "synthetic prompt"); err != nil {
		t.Fatal(err)
	}
	return store, []domain.Session{ready}
}

// Isolated adapter fixture: no native CLI, tmux, live session or network.
func TestAcceptedProofAdapterProcess(t *testing.T) {
	mode := os.Getenv("BRIA_ACCEPTED_PROOF_HELPER")
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
			emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
		case "interrupt":
			if mode == "eof" {
				os.Exit(1)
			}
			status, code := "interrupted", "interrupted"
			if mode == "failed" {
				status, code = "failed", "authentication_failed"
			}
			emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": status, "error_code": code})
		case "close":
			os.Exit(0)
		default:
			os.Exit(83)
		}
	}
	os.Exit(0)
}

type acceptedExitRuntime struct{}

func (acceptedExitRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, sessionruntime.ErrProtocol
}
func (acceptedExitRuntime) SubmitWithCallbacks(_ context.Context, _ domain.SessionID, _ string, c sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if err := c.OnAccepted(c.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	return sessionruntime.TurnResult{}, sessionruntime.ErrProtocol
}

func TestAcceptedUncertainExitCannotFinishLifecycleBeforeSupervisor(t *testing.T) {
	ctx := context.Background()
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111")
	lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	notices := make(chan telegramcontroller.Notification, 8)
	c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, acceptedExitRuntime{}, archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error { notices <- n; return nil }), telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessions[0].ID()}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.HandleSemanticMessage(ctx, coordinator.Update{ID: 801, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "synthetic accepted request"}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case n := <-notices:
			if n.Kind != telegramcontroller.NotificationError {
				continue
			}
			current, err := store.Load(ctx, sessions[0].ID())
			if err != nil {
				t.Fatal(err)
			}
			if current.Status() == domain.SessionReady {
				t.Fatal("accepted uncertain exit became ready before accepted-turn reconciliation")
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("missing terminal error notification")
		}
	}
}
