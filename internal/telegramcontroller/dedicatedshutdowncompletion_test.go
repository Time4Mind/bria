package telegramcontroller_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

// Uses the real Starter and controller. The synthetic adapter acknowledges both
// inputs, then holds interruption proof until Close has cancelled the root.
func TestShutdownCompletionWaitsForMainAndSteerTerminal(t *testing.T) {
	for _, mode := range []string{"interrupted", "exit", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			interrupted, releasePath := filepath.Join(root, "interrupted"), filepath.Join(root, "release")
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestShutdownCompletionAdapterProcess$"}, Env: []string{
					"BRIA_SHUTDOWN_COMPLETION_HELPER=" + mode, "BRIA_SHUTDOWN_COMPLETION_DIR=" + root,
				}},
			}, sessionruntime.Options{GracefulCloseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			request := app.StartSessionRequest{SessionID: "33333333-3333-4333-9333-333333333339", ComputerID: "local", Provider: domain.ProviderCodex, Workdir: root, Mode: app.SessionStartNew}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			binding, err := starter.Start(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = starter.Abort(context.Background(), request, binding) })
			ready := readySession(t, string(request.SessionID), domain.ProviderCodex, root, binding.SessionID, binding.Generation)
			controller := newController(t, nil, newLockedSessions(ready), starter, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}})
			var releaseOnce sync.Once
			release := func() {
				releaseOnce.Do(func() {
					if err := os.WriteFile(releasePath, []byte("release"), 0600); err != nil {
						t.Error(err)
					}
				})
			}
			t.Cleanup(func() {
				release()
				closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				if err := controller.Close(closeCtx); err != nil {
					t.Error(err)
				}
			})
			completed := make(chan telegramcontroller.DurableInputProcessReceipt, 4)
			for index, message := range []string{"root", "steer"} {
				input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: message, Sequence: uint64(index + 1), Payload: []byte(message)}
				receipt, err := controller.ProcessDurableInput(ctx, input, telegramcontroller.DurableInputCallbacks{
					OnAccepted: func(_ context.Context, acceptance telegramcontroller.DurableInputAcceptance) error {
						if acceptance.SessionID != input.SessionID || acceptance.MessageID != input.MessageID || acceptance.Sequence != input.Sequence {
							return fmt.Errorf("wrong accepted identity")
						}
						return nil
					},
					OnCompleted: func(callbackCtx context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
						if err := callbackCtx.Err(); err != nil {
							return err
						}
						completed <- receipt
						return nil
					},
				})
				if err != nil || !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputPending {
					t.Fatalf("early %s receipt=%+v err=%v", message, receipt, err)
				}
			}
			closed := make(chan error, 1)
			go func() {
				shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				closed <- controller.Close(shutdownCtx)
			}()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(interrupted); err == nil {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("adapter did not receive root interruption")
				}
			}
			select {
			case receipt := <-completed:
				t.Fatalf("completion before terminal drain release: %+v", receipt)
			case err := <-closed:
				t.Fatalf("Close returned before terminal drain release: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			release()
			want := telegramcontroller.DurableInputTerminalFailed
			if mode != "interrupted" {
				want = telegramcontroller.DurableInputUnknown
			}
			seen := map[string]bool{}
			for len(seen) < 2 {
				select {
				case receipt := <-completed:
					sequence := uint64(1)
					if receipt.MessageID == "steer" {
						sequence = 2
					}
					if receipt.MessageID != "root" && receipt.MessageID != "steer" || seen[receipt.MessageID] || receipt.SessionID != ready.ID() || receipt.Sequence != sequence || !receipt.Accepted || receipt.Completion != want {
						t.Fatalf("terminal receipt=%+v want=%s", receipt, want)
					}
					seen[receipt.MessageID] = true
				case <-ctx.Done():
					t.Fatal("missing main/steer terminal completion")
				}
			}
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("Close blocked after producer completed")
			}
			select {
			case receipt := <-completed:
				t.Fatalf("duplicate completion: %+v", receipt)
			default:
			}
		})
	}
}

func TestShutdownCompletionAdapterProcess(t *testing.T) {
	mode := os.Getenv("BRIA_SHUTDOWN_COMPLETION_HELPER")
	if mode == "" {
		return
	}
	emit := func(value any) {
		if json.NewEncoder(os.Stdout).Encode(value) != nil {
			os.Exit(81)
		}
	}
	emit(map[string]any{"protocol": 1, "type": "ready", "provider_session_id": "shutdown-provider", "readiness": "protocol", "authentication": "unknown"})
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message struct{ Type, RequestID, MessageID string }
		var wire struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			MessageID string `json:"message_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &wire) != nil {
			os.Exit(82)
		}
		message.Type, message.RequestID, message.MessageID = wire.Type, wire.RequestID, wire.MessageID
		switch message.Type {
		case "submit", "steer":
			emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
		case "interrupt":
			root := os.Getenv("BRIA_SHUTDOWN_COMPLETION_DIR")
			if os.WriteFile(filepath.Join(root, "interrupted"), []byte("received"), 0600) != nil {
				os.Exit(83)
			}
			deadline := time.Now().Add(4 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					os.Exit(84)
				}
				time.Sleep(time.Millisecond)
			}
			if mode == "exit" {
				os.Exit(0)
			}
			if mode == "timeout" {
				time.Sleep(5 * time.Second)
				os.Exit(85)
			}
			emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "interrupted", "error_code": "interrupted"})
		case "close":
			os.Exit(0)
		default:
			os.Exit(86)
		}
	}
	os.Exit(0)
}
