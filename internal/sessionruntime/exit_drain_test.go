package sessionruntime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

func TestExitedAdapterDrainsBufferedEventsAndTerminalProof(t *testing.T) {
	for _, mode := range []string{"event-eof", "completed", "interrupted", "steer-completed", "cancel-steer-interrupted", "stop-interrupted"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			rootCtx, rootCancel := context.WithCancel(ctx)
			defer rootCancel()
			steering := mode == "steer-completed" || mode == "cancel-steer-interrupted"
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestExitDrainAdapterProcess$"}, Env: []string{"BRIA_EXIT_DRAIN_HELPER=" + mode}},
			}, sessionruntime.Options{GracefulCloseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			request := app.StartSessionRequest{SessionID: "synthetic-exit-drain", ComputerID: "local", Provider: domain.ProviderCodex, Workdir: t.TempDir(), Mode: app.SessionStartNew}
			binding, err := starter.Start(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			defer starter.Abort(context.Background(), request, binding)
			accepted, reaped := false, false
			steerAccepted := false
			steerResult := make(chan error, 1)
			stopResult := make(chan error, 1)
			var events []sessionruntime.TurnEvent
			result, submitErr := starter.SubmitWithCallbacks(rootCtx, request.SessionID, "synthetic input", sessionruntime.TurnCallbacks{
				MessageID: "exact-root-message",
				OnAccepted: func(messageID string) error {
					if messageID != "exact-root-message" || accepted {
						return errors.New("invalid exact acceptance")
					}
					accepted = true
					if steering {
						go func() {
							steerResult <- starter.SubmitCurrentWithCallbacks(ctx, request.SessionID, sessionruntime.StructuredInput{Text: "synthetic steer"}, sessionruntime.TurnCallbacks{
								MessageID: "exact-steer-message",
								OnAccepted: func(messageID string) error {
									if messageID != "exact-steer-message" || steerAccepted {
										return errors.New("invalid exact steer acceptance")
									}
									steerAccepted = true
									return nil
								},
							})
						}()
					}
					if mode == "stop-interrupted" {
						go func() { stopResult <- starter.StopCurrent(ctx, request.SessionID) }()
					}
					// The helper writes the entire short stream and exits. Reaping
					// is the barrier: output and process-done are both available
					// before the submit consumer can resume, without sleeps.
					if err := starter.Wait(ctx, request.SessionID, binding); err != nil {
						return err
					}
					reaped = true
					if mode == "cancel-steer-interrupted" {
						// Only root submission is cancelled. The registered steer
						// keeps its live parent context while buffered ACK drains.
						rootCancel()
					}
					return nil
				},
				OnEvent: func(event sessionruntime.TurnEvent) error {
					events = append(events, event)
					return nil
				},
			})
			if !accepted || !reaped {
				t.Fatalf("exact accepted=%v process reaped=%v; buffered acceptance lost", accepted, reaped)
			}
			if steering {
				select {
				case err := <-steerResult:
					if err != nil || !steerAccepted {
						t.Errorf("buffered exact steer acceptance lost after reap: error=%v accepted=%v", err != nil, steerAccepted)
					}
				case <-ctx.Done():
					t.Fatal("steer submission did not settle after process reap")
				}
			}
			if mode == "stop-interrupted" {
				select {
				case err := <-stopResult:
					if err != nil {
						t.Errorf("StopCurrent lost correlated interrupted proof after reap: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("StopCurrent waiter did not settle after process reap")
				}
			}
			wantEvents := []sessionruntime.TurnEvent{{Kind: sessionruntime.EventCommentary, Text: "synthetic first"}, {Kind: sessionruntime.EventTool, Text: "synthetic second"}}
			if mode != "cancel-steer-interrupted" && !reflect.DeepEqual(events, wantEvents) {
				t.Errorf("reaped adapter lost/reordered OnEvent: count=%d want=2 exact_order=%v", len(events), reflect.DeepEqual(events, wantEvents))
			}
			switch mode {
			case "event-eof":
				if submitErr == nil || errors.Is(submitErr, sessionruntime.ErrTurnFailed) || result.TerminalStatus != "" || result.Final != "" {
					t.Errorf("EOF invented terminal proof: error=%v terminal=%q final_present=%v", submitErr != nil, result.TerminalStatus, result.Final != "")
				}
			case "completed", "steer-completed":
				if submitErr != nil || result.TerminalStatus != sessionruntime.StatusCompleted || result.Final != "synthetic retained final" || !reflect.DeepEqual(result.Events, wantEvents) {
					t.Errorf("buffered completion lost: error=%v terminal=%q exact_final=%v event_count=%d", submitErr != nil, result.TerminalStatus, result.Final == "synthetic retained final", len(result.Events))
				}
			case "interrupted", "stop-interrupted":
				if !errors.Is(submitErr, sessionruntime.ErrTurnFailed) || result.TerminalStatus != sessionruntime.StatusInterrupted || result.ErrorCode != sessionruntime.ErrorInterrupted || result.Final != "" || !reflect.DeepEqual(result.Events, wantEvents) {
					t.Errorf("buffered interrupted proof lost: terminal_error=%v terminal=%q error_code=%q final_present=%v event_count=%d", errors.Is(submitErr, sessionruntime.ErrTurnFailed), result.TerminalStatus, result.ErrorCode, result.Final != "", len(result.Events))
				}
			case "cancel-steer-interrupted":
				cancelled := errors.Is(submitErr, context.Canceled)
				if !cancelled && !errors.Is(submitErr, sessionruntime.ErrTurnFailed) || result.TerminalStatus != sessionruntime.StatusInterrupted || result.ErrorCode != sessionruntime.ErrorInterrupted || result.Final != "" {
					t.Errorf("cancelled root lost buffered interrupted proof: cancelled=%v terminal=%q error_code=%q final_present=%v", cancelled, result.TerminalStatus, result.ErrorCode, result.Final != "")
				}
				t.Logf("root cancellation drain observed=%v; exact steer ACK=%v", cancelled, steerAccepted)
			}
		})
	}
}

// Isolated protocol child, selected only by the parent test's explicit env.
// No native provider, network, live session or external state is touched.
func TestExitDrainAdapterProcess(t *testing.T) {
	mode := os.Getenv("BRIA_EXIT_DRAIN_HELPER")
	if mode == "" {
		return
	}
	emit := func(value any) {
		if json.NewEncoder(os.Stdout).Encode(value) != nil {
			os.Exit(81)
		}
	}
	emit(map[string]any{"protocol": 1, "type": "ready", "provider_session_id": "synthetic-exit-provider", "readiness": "protocol", "authentication": "unknown"})
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(82)
	}
	var message struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Type != "submit" {
		os.Exit(83)
	}
	emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
	if mode == "steer-completed" || mode == "cancel-steer-interrupted" {
		var steer struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			MessageID string `json:"message_id"`
		}
		if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &steer) != nil || steer.Type != "steer" || steer.MessageID != "exact-steer-message" {
			os.Exit(85)
		}
		emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": steer.RequestID, "message_id": steer.MessageID})
	}
	if mode == "stop-interrupted" {
		var interrupt struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &interrupt) != nil || interrupt.Type != "interrupt" || interrupt.RequestID != message.RequestID {
			os.Exit(86)
		}
	}
	emit(map[string]any{"protocol": 1, "type": "event", "request_id": message.RequestID, "kind": "commentary", "text": "synthetic first"})
	emit(map[string]any{"protocol": 1, "type": "event", "request_id": message.RequestID, "kind": "tool", "text": "synthetic second"})
	switch mode {
	case "event-eof":
	case "completed", "steer-completed":
		emit(map[string]any{"protocol": 1, "type": "final", "request_id": message.RequestID, "text": "synthetic retained final"})
		emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "completed"})
	case "interrupted", "cancel-steer-interrupted", "stop-interrupted":
		emit(map[string]any{"protocol": 1, "type": "final", "request_id": message.RequestID, "text": "must not publish interrupted candidate"})
		emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "interrupted", "error_code": "interrupted"})
	default:
		os.Exit(84)
	}
	os.Exit(0)
}
