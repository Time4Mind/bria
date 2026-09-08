package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/observability"
	"bria/internal/observabilitycomposition"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
)

type a25DiagnosticResolver struct{}

func (a25DiagnosticResolver) ProviderForSession(context.Context, domain.SessionID) (domain.Provider, error) {
	return domain.ProviderCodex, nil
}

func TestA25NativeExitDiagnosticReachesPhysicalSafeLogWithExactCorrelation(t *testing.T) {
	a25NativeExitDiagnostic(t, false)
}

func TestA25NativeExitAfterAcceptanceWaitPreservesBufferedModelEvent(t *testing.T) {
	a25NativeExitDiagnostic(t, true)
}

func a25NativeExitDiagnostic(t *testing.T, waitForExit bool) {
	t.Helper()
	directory := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observability.New(logger)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ session, message string }{
		{"11111111-1111-4111-8111-111111111111", "a25-request-one"},
		{"11111111-1111-4111-8111-111111111111", "a25-request-two"},
		{"22222222-2222-4222-8222-222222222222", "a25-request-one"},
	}
	const privatePrompt = a25FailurePrivatePrompt
	for _, tc := range cases {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				domain.ProviderCodex: {
					Path: os.Args[0],
					Args: []string{"-test.run=^TestA25RuntimeFailureLogAdapterProcess$"},
					Env:  []string{"BRIA_A25_FAILURE_LOG_HELPER=1"},
				},
			}, sessionruntime.Options{})
			if err != nil {
				t.Fatal(err)
			}
			request := app.StartSessionRequest{
				SessionID: domain.SessionID(tc.session), ComputerID: "local",
				Provider: domain.ProviderCodex, Workdir: t.TempDir(), Mode: app.SessionStartNew,
			}
			binding, err := starter.Start(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			defer starter.Abort(context.Background(), request, binding)
			wrapped, err := observabilitycomposition.New(starter, recorder, a25DiagnosticResolver{})
			if err != nil {
				t.Fatal(err)
			}
			accepted := ""
			modelEvents := 0
			result, err := wrapped.SubmitWithCallbacks(ctx, request.SessionID, privatePrompt, sessionruntime.TurnCallbacks{
				MessageID: tc.message, OnAccepted: func(id string) error {
					accepted = id
					if waitForExit {
						// The child writes commentary before exiting. Wait is the
						// public exact-generation reap barrier, not a timing sleep:
						// stdout has been read but the event is not consumed yet.
						return starter.Wait(ctx, request.SessionID, binding)
					}
					return nil
				},
				OnEvent: func(event sessionruntime.TurnEvent) error {
					if event.Kind != sessionruntime.EventCommentary || event.Text != a25FailurePrivateModelText {
						return fmt.Errorf("unexpected synthetic model event")
					}
					modelEvents++
					return nil
				},
			})
			if accepted != tc.message || modelEvents != 1 || err == nil || result.Final != "" || sessionruntime.RuntimeFailureClass(err) != "native_transcript_record_too_large" {
				t.Fatalf("acceptance=%q modelEvents=%d final_bytes=%d class=%q err=%v", accepted, modelEvents, len(result.Final), sessionruntime.RuntimeFailureClass(err), err)
			}
		}()
	}
	raw, err := os.ReadFile(filepath.Join(directory, "service.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{privatePrompt, a25FailurePrivateModel, a25FailurePrivateModelText, "private-payload", "never-log", "bria-native-startup:", cases[0].session, cases[2].session, cases[0].message, cases[1].message} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("physical diagnostic log leaked a raw correlation input or payload")
		}
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("terminal records=%d, want %d", len(lines), len(cases))
	}
	seen := map[string]bool{}
	for i, line := range lines {
		var event safelog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		tc := cases[i]
		// Independent encoding of the documented session/request/stage contract.
		digest := sha256.Sum256([]byte("bria-observability-v1\x00" + tc.session + "\x00" + tc.message + "\x00provider.codex.submit"))
		wantID := "c_" + hex.EncodeToString(digest[:])
		if event.EntityID != wantID || seen[event.EntityID] || event.Class != safelog.Service || event.Type != "timing.terminal" || event.Result != "error" || event.ErrorCategory != "native_transcript_record_too_large" || event.Error != "" || event.Fields["operation"] != "provider.codex.submit" || event.Fields["provider_accept_ms"] == "" {
			t.Fatalf("terminal record %d lost safe classification or exact correlation: %+v", i, event)
		}
		seen[event.EntityID] = true
	}
}

const (
	a25FailurePrivatePrompt    = "synthetic-private-prompt-not-for-logs"
	a25FailurePrivateModel     = "a25-private-model-do-not-log"
	a25FailurePrivateModelText = "a25-private-model-text-not-for-logs"
)

// This test executable is the adapter. No provider binary, live session, or
// application startup is involved; only this exact test runs in the child.
func TestA25RuntimeFailureLogAdapterProcess(t *testing.T) {
	if os.Getenv("BRIA_A25_FAILURE_LOG_HELPER") != "1" {
		return
	}
	emit := func(value any) {
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			os.Exit(81)
		}
	}
	emit(map[string]any{
		"protocol": 1, "type": "ready", "provider_session_id": "a25-fixture-provider-session",
		"readiness": "protocol", "authentication": "unknown", "model": a25FailurePrivateModel,
	})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		var request struct {
			Protocol  int    `json:"protocol"`
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			MessageID string `json:"message_id"`
			Text      string `json:"text"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Protocol != 1 {
			os.Exit(82)
		}
		if request.Type == "close" {
			os.Exit(0)
		}
		if request.Type != "submit" || request.RequestID == "" || request.MessageID == "" || request.Text != a25FailurePrivatePrompt {
			os.Exit(83)
		}
		emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": request.RequestID, "message_id": request.MessageID})
		emit(map[string]any{"protocol": 1, "type": "event", "request_id": request.RequestID, "kind": "commentary", "text": a25FailurePrivateModelText})
		fmt.Fprintln(os.Stderr, "private-payload=never-log")
		fmt.Fprintln(os.Stderr, "bria-native-startup:native_transcript_record_too_large")
		os.Exit(1)
	}
	os.Exit(84)
}
