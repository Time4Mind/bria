package codex_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"bria/internal/provider/codex"
	"bria/internal/runtimeprotocol"
)

func TestAdapterReadySubmitFinalAndConfirmedClose(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "happy", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=happy",
				"BRIA_FLOOD_STDERR=1",
			},
			Workdir: workdir,
			ClientInfo: codex.ClientInfo{
				Name:    "bria-codex-adapter-test",
				Version: "0.1.0",
			},
		})
	}()

	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol":            float64(1),
		"type":                "ready",
		"provider_session_id": "provider-thread-1",
		"readiness":           "protocol",
		"authentication":      "unknown",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"request_1","text":"hello"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol":   float64(1),
		"type":       "accepted",
		"request_id": "request_1",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol":   float64(1),
		"type":       "event",
		"request_id": "request_1",
		"kind":       "commentary",
		"text":       "working",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol":   float64(1),
		"type":       "final",
		"request_id": "request_1",
		"text":       "done",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol":   float64(1),
		"type":       "completed",
		"request_id": "request_1",
		"status":     "completed",
	})

	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not confirm raw child exit after close")
	}
}

func TestAdapterVerifiesAndEncodesOfficialLocalImageWithoutPromptPath(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "photo.png")
	content := []byte("photo bytes")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=local-image", "BRIA_EXPECT_IMAGE_PATH=" + path,
			},
			Workdir: workdir, ClientInfo: codex.ClientInfo{Name: "bria", Version: "test"},
		})
	}()
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "ready", "provider_session_id": "provider-thread-1",
		"readiness": "protocol", "authentication": "unknown",
	})
	line, err := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{
		Protocol: 1, Type: runtimeprotocol.TypeSubmit, RequestID: "photo-1", MessageID: "telegram:photo:1", Text: "",
		Attachments: []runtimeprotocol.LocalAttachment{{Path: path, Size: int64(len(content)), SHA256: digest}},
	}, runtimeprotocol.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeParentLine(t, adapterInput, strings.TrimSpace(string(line)))
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted", "request_id": "photo-1", "message_id": "telegram:photo:1"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "event", "request_id": "photo-1", "kind": "commentary", "text": "working"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "final", "request_id": "photo-1", "text": "done"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "completed", "request_id": "photo-1", "status": "completed"})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	if err := <-runDone; err != nil {
		t.Fatalf("RunAdapter() error = %v", err)
	}
}

func TestAdapterRejectsChangedLocalImageBeforeProviderAcceptance(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "private-photo.png")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv:     []string{"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_RAW_HELPER_MODE=stall"},
			Workdir:    workdir, ClientInfo: codex.ClientInfo{Name: "bria", Version: "test"},
		})
	}()
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "ready", "provider_session_id": "provider-thread-1",
		"readiness": "protocol", "authentication": "unknown",
	})
	line, err := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{
		Protocol: 1, Type: runtimeprotocol.TypeSubmit, RequestID: "photo-bad", Text: "inspect",
		Attachments: []runtimeprotocol.LocalAttachment{{
			Path: path, Size: int64(len("changed")),
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}, runtimeprotocol.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeParentLine(t, adapterInput, strings.TrimSpace(string(line)))
	select {
	case err := <-runDone:
		if !errors.Is(err, codex.ErrAdapterProtocol) {
			t.Fatalf("RunAdapter() error = %v, want ErrAdapterProtocol", err)
		}
		if strings.Contains(err.Error(), path) {
			t.Fatalf("adapter error leaked resolved path: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("changed attachment did not fail closed")
	}
}

func TestAdapterRelaysCorrelatedQuestionAndReturnsAnswerToCodex(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv:     []string{"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_RAW_HELPER_MODE=interactive-question"},
			Workdir:    workdir, ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"request-question","message_id":"telegram:42","text":"ask"}`)
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted", "request_id": "request-question", "message_id": "telegram:42"})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "interaction_request", "request_id": "request-question",
		"interaction_request": map[string]any{
			"id": "number:901", "kind": "question", "thread_id": "provider-thread-1", "turn_id": "turn-1", "item_id": "item-1", "blocking": true,
			"questions": []any{map[string]any{"id": "choice", "header": "Pick", "text": "Pick one", "options": []any{map[string]any{"label": "First", "description": "first choice"}}}},
		},
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"interaction_response","request_id":"request-question","interaction_response":{"id":"number:901","outcome":"answered","answers":{"choice":["First"]}}}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "interaction_response_accepted", "provider_session_id": "provider-thread-1",
		"request_id": "request-question", "message_id": "telegram:42", "interaction_id": "number:901",
	})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "final", "request_id": "request-question", "text": "selected First"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "completed", "request_id": "request-question", "status": "completed"})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not close")
	}
}

func TestAdapterReconcilesAcceptedTurnsThroughOfficialPersistedHistory(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv:     []string{"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_EXPECT_RESUME_THREAD_ID=provider-thread-1", "BRIA_RAW_HELPER_MODE=reconcile"},
			Workdir:    workdir, ResumeThreadID: "provider-thread-1", ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"reconcile_accepted_turns","request_id":"reconcile-1"}`)
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted_turn", "request_id": "reconcile-1", "message_id": "m-complete", "status": "completed"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted_turn", "request_id": "reconcile-1", "message_id": "m-live", "status": "unknown"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "reconciliation_completed", "request_id": "reconcile-1"})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not close")
	}
}

func TestAdapterResumesExactPersistedThreadBeforeReportingReady(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand:     []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv:         []string{"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_EXPECT_RESUME_THREAD_ID=provider-thread-persisted"},
			Workdir:        workdir,
			ResumeThreadID: "provider-thread-persisted",
			ClientInfo:     codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()

	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "ready", "provider_session_id": "provider-thread-persisted",
		"readiness": "protocol", "authentication": "unknown",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter(resume) error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter(resume) did not close")
	}
}

func TestAdapterRejectsResumeWhenProviderReturnsDifferentThread(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	defer adapterInput.Close()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_EXPECT_RESUME_THREAD_ID=provider-thread-persisted", "BRIA_RETURN_THREAD_ID=other-thread",
			},
			Workdir: workdir, ResumeThreadID: "provider-thread-persisted",
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()

	select {
	case err := <-runDone:
		if !errors.Is(err, codex.ErrRawProcess) {
			t.Fatalf("RunAdapter(mismatched resume) error = %v, want ErrRawProcess", err)
		}
	case line := <-adapterOutput.lines:
		t.Fatalf("mismatched resume published output before failing: %s", line)
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter(mismatched resume) did not fail")
	}
}

func TestAdapterQueuesSecondSubmitUntilActiveTurnCompletes(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_RAW_HELPER_MODE=queue",
			},
			Workdir: workdir, ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()

	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"first","text":"one"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "first",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"second","text":"two"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "final", "request_id": "first", "text": "done-one",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "first", "status": "completed",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "second",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "final", "request_id": "second", "text": "done-two",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "second", "status": "completed",
	})

	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error after queued submit = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not close after queued submit")
	}
}

func TestAdapterDoesNotStartQueuedSubmitAfterProtocolCorruption(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	defer adapterInput.Close()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_RAW_HELPER_MODE=protocol-error",
			},
			Workdir: workdir, ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()

	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"first","text":"one"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "first",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"second","text":"two"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "first",
		"status": "failed", "error_code": "protocol_error",
	})
	select {
	case err := <-runDone:
		if !errors.Is(err, codex.ErrAdapterProtocol) {
			t.Fatalf("RunAdapter() error = %v, want ErrAdapterProtocol", err)
		}
	case line := <-adapterOutput.lines:
		t.Fatalf("queued request produced output after protocol corruption: %s", line)
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not fail after protocol corruption")
	}
}

func TestAdapterPreservesInterruptsForMultipleQueuedSubmits(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_RAW_HELPER_MODE=queue-interrupt",
			},
			Workdir: workdir, ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()

	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"first","text":"one"}`)
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted", "request_id": "first"})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"second","text":"two"}`)
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"third","text":"three"}`)
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"interrupt","request_id":"second"}`)
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"interrupt","request_id":"third"}`)
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "final", "request_id": "first", "text": "done-one"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "completed", "request_id": "first", "status": "completed"})
	for _, requestID := range []string{"second", "third"} {
		assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted", "request_id": requestID})
		assertJSONLine(t, adapterOutput, map[string]any{
			"protocol": float64(1), "type": "completed", "request_id": requestID,
			"status": "interrupted", "error_code": "interrupted",
		})
	}
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not close after queued interrupts")
	}
}

func TestAdapterContinuesQueuedSubmitAfterAuthoritativeProviderFailure(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1", "BRIA_EXPECT_WORKDIR=" + workdir, "BRIA_RAW_HELPER_MODE=queue-failure",
			},
			Workdir: workdir, ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()

	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"first","text":"one"}`)
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted", "request_id": "first"})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"second","text":"two"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "first", "status": "failed", "error_code": "provider_error",
	})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "accepted", "request_id": "second"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "final", "request_id": "second", "text": "done-two"})
	assertJSONLine(t, adapterOutput, map[string]any{"protocol": float64(1), "type": "completed", "request_id": "second", "status": "completed"})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not close after queued provider failure")
	}
}

func TestRawCodexAppServerHelperProcess(t *testing.T) {
	if os.Getenv("BRIA_CODEX_RAW_HELPER") != "1" {
		return
	}
	if os.Getenv("BRIA_CODEX_RAW_GRANDCHILD") == "1" {
		readyPath := os.Getenv("BRIA_RAW_GRANDCHILD_READY_PATH")
		if readyPath == "" || os.WriteFile(readyPath, []byte("ready"), 0o600) != nil {
			os.Exit(89)
		}
		select {}
	}
	runRawHelper()
	os.Exit(0)
}

func runRawHelper() {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "grandchild" || os.Getenv("BRIA_RAW_HELPER_MODE") == "leader-exit" {
		grandchild := exec.Command(os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$")
		grandchild.Env = append(os.Environ(), "BRIA_CODEX_RAW_GRANDCHILD=1")
		if grandchild.Start() != nil {
			os.Exit(86)
		}
		pidPath := os.Getenv("BRIA_RAW_GRANDCHILD_PID_PATH")
		pids := strconv.Itoa(os.Getpid()) + " " + strconv.Itoa(grandchild.Process.Pid)
		if os.WriteFile(pidPath, []byte(pids), 0o600) != nil {
			_ = grandchild.Process.Kill()
			os.Exit(87)
		}
	}
	if os.Getenv("BRIA_FLOOD_STDERR") == "1" {
		chunk := strings.Repeat("x", 32*1024)
		for range 64 {
			if _, err := fmt.Fprint(os.Stderr, chunk); err != nil {
				os.Exit(79)
			}
		}
	}

	request := readRawObject(reader)
	requireRaw(request["method"] == "initialize")
	writeRawResult(encoder, request["id"], map[string]any{"userAgent": "codex-cli/0.152.1"})

	request = readRawObject(reader)
	requireRaw(request["method"] == "initialized")

	request = readRawObject(reader)
	expectedResumeThreadID := os.Getenv("BRIA_EXPECT_RESUME_THREAD_ID")
	if expectedResumeThreadID == "" {
		requireRaw(request["method"] == "thread/start")
	} else {
		requireRaw(request["method"] == "thread/resume")
	}
	params, ok := request["params"].(map[string]any)
	requireRaw(ok)
	requireRaw(params["cwd"] == os.Getenv("BRIA_EXPECT_WORKDIR"))
	if expectedResumeThreadID == "" {
		requireRaw(params["ephemeral"] == false)
	} else {
		requireRaw(params["threadId"] == expectedResumeThreadID)
		_, hasEphemeral := params["ephemeral"]
		requireRaw(!hasEphemeral)
	}
	_, hasApproval := params["approvalPolicy"]
	_, hasSandbox := params["sandbox"]
	requireRaw(!hasApproval && !hasSandbox)
	providerThreadID := "provider-thread-1"
	if expectedResumeThreadID != "" {
		providerThreadID = expectedResumeThreadID
	}
	if returnedThreadID := os.Getenv("BRIA_RETURN_THREAD_ID"); returnedThreadID != "" {
		providerThreadID = returnedThreadID
	}
	writeRawResult(encoder, request["id"], map[string]any{
		"thread": map[string]any{
			"id":        providerThreadID,
			"sessionId": "provider-session-1",
			"ephemeral": false,
			"cwd":       os.Getenv("BRIA_EXPECT_WORKDIR"),
		},
		"approvalPolicy": "untrusted",
		"sandbox": map[string]any{
			"type":          "readOnly",
			"networkAccess": false,
		},
	})
	if expectedResumeThreadID != "" && os.Getenv("BRIA_RAW_HELPER_MODE") != "reconcile" {
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "reconcile" {
		request = readRawObject(reader)
		requireRaw(request["method"] == "thread/turns/list")
		params, ok = request["params"].(map[string]any)
		requireRaw(ok && params["threadId"] == providerThreadID && params["itemsView"] == "full" && params["sortDirection"] == "desc")
		writeRawResult(encoder, request["id"], map[string]any{"data": []any{
			map[string]any{"id": "turn-2", "itemsView": "full", "status": "completed", "items": []any{map[string]any{"type": "userMessage", "id": "u2", "clientId": "m-complete", "content": []any{}}}},
			map[string]any{"id": "turn-1", "itemsView": "full", "status": "inProgress", "items": []any{map[string]any{"type": "userMessage", "id": "u1", "clientId": "m-live", "content": []any{}}}},
		}, "nextCursor": nil})
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "queue" || os.Getenv("BRIA_RAW_HELPER_MODE") == "queue-failure" {
		runQueuedRawHelper(reader, encoder)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "queue-interrupt" {
		runQueuedInterruptRawHelper(reader, encoder)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "leader-exit" {
		if os.WriteFile(os.Getenv("BRIA_RAW_LEADER_EXIT_PATH"), []byte("exiting"), 0o600) != nil {
			os.Exit(88)
		}
		return
	}

	request = readRawObject(reader)
	requireRaw(request["method"] == "turn/start")
	params, ok = request["params"].(map[string]any)
	requireRaw(ok && params["threadId"] == "provider-thread-1")
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "local-image" {
		input, ok := params["input"].([]any)
		requireRaw(ok && len(input) == 1)
		image, ok := input[0].(map[string]any)
		requireRaw(ok && image["type"] == "localImage" && image["path"] == os.Getenv("BRIA_EXPECT_IMAGE_PATH"))
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "early-interrupt" || os.Getenv("BRIA_RAW_HELPER_MODE") == "buffered-terminal" {
		time.Sleep(100 * time.Millisecond)
	}
	writeRawResult(encoder, request["id"], map[string]any{
		"turn": map[string]any{"id": "turn-1", "status": "inProgress", "error": nil},
	})
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "protocol-error" {
		time.Sleep(100 * time.Millisecond)
		_, _ = fmt.Fprintln(os.Stdout, "not-json")
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "stall" {
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "buffered-terminal" {
		writeRawNotification(encoder, "item/completed", map[string]any{
			"threadId": "provider-thread-1", "turnId": "turn-1",
			"item": map[string]any{"type": "agentMessage", "id": "buffered-final", "text": "do not publish", "phase": "final_answer"},
		})
		writeRawNotification(encoder, "turn/completed", map[string]any{
			"threadId": "provider-thread-1",
			"turn":     map[string]any{"id": "turn-1", "status": "completed", "error": nil},
		})
		request = readRawObject(reader)
		requireRaw(request["method"] == "turn/interrupt")
		params, ok = request["params"].(map[string]any)
		requireRaw(ok && params["threadId"] == "provider-thread-1" && params["turnId"] == "turn-1")
		writeRawResult(encoder, request["id"], map[string]any{})
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "interrupt" || os.Getenv("BRIA_RAW_HELPER_MODE") == "early-interrupt" {
		writeRawNotification(encoder, "turn/started", map[string]any{
			"threadId": "provider-thread-1",
			"turn":     map[string]any{"id": "turn-1"},
		})
		request = readRawObject(reader)
		requireRaw(request["method"] == "turn/interrupt")
		params, ok = request["params"].(map[string]any)
		requireRaw(ok && params["threadId"] == "provider-thread-1" && params["turnId"] == "turn-1")
		writeRawResult(encoder, request["id"], map[string]any{})
		writeRawNotification(encoder, "item/completed", map[string]any{
			"threadId": "provider-thread-1",
			"turnId":   "turn-1",
			"item": map[string]any{
				"type":  "agentMessage",
				"id":    "discarded-final",
				"text":  "must not publish",
				"phase": "final_answer",
			},
		})
		writeRawNotification(encoder, "turn/completed", map[string]any{
			"threadId": "provider-thread-1",
			"turn": map[string]any{
				"id":     "turn-1",
				"status": "interrupted",
				"error":  nil,
			},
		})
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "failed" {
		writeRawNotification(encoder, "item/completed", map[string]any{
			"threadId": "provider-thread-1",
			"turnId":   "turn-1",
			"item": map[string]any{
				"type":  "agentMessage",
				"id":    "discarded-final",
				"text":  "must not publish",
				"phase": "final_answer",
			},
		})
		writeRawNotification(encoder, "turn/completed", map[string]any{
			"threadId": "provider-thread-1",
			"turn": map[string]any{
				"id":     "turn-1",
				"status": "failed",
				"error":  map[string]any{"message": "secret-token /private/path"},
			},
		})
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "server-request" {
		if encoder.Encode(map[string]any{
			"id": 901, "method": "item/tool/requestUserInput",
			"params": map[string]any{"threadId": "provider-thread-1", "turnId": "turn-1"},
		}) != nil {
			os.Exit(85)
		}
		request = readRawObject(reader)
		requireRaw(request["method"] == "turn/interrupt")
		params, ok = request["params"].(map[string]any)
		requireRaw(ok && params["threadId"] == "provider-thread-1" && params["turnId"] == "turn-1")
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	if os.Getenv("BRIA_RAW_HELPER_MODE") == "interactive-question" {
		if encoder.Encode(map[string]any{
			"id": 901, "method": "item/tool/requestUserInput",
			"params": map[string]any{
				"threadId": "provider-thread-1", "turnId": "turn-1", "itemId": "item-1", "isBlocking": true,
				"questions": []any{map[string]any{"id": "choice", "header": "Pick", "question": "Pick one", "options": []any{map[string]any{"label": "First", "description": "first choice"}}, "isOther": false, "isSecret": false}},
			},
		}) != nil {
			os.Exit(86)
		}
		response := readRawObject(reader)
		requireRaw(response["id"] == float64(901))
		result, ok := response["result"].(map[string]any)
		requireRaw(ok)
		answers, ok := result["answers"].(map[string]any)
		requireRaw(ok)
		choice, ok := answers["choice"].(map[string]any)
		requireRaw(ok)
		values, ok := choice["answers"].([]any)
		requireRaw(ok && len(values) == 1 && values[0] == "First")
		writeRawNotification(encoder, "item/completed", map[string]any{"threadId": "provider-thread-1", "turnId": "turn-1", "item": map[string]any{"type": "agentMessage", "id": "final-1", "text": "selected First", "phase": "final_answer"}})
		writeRawNotification(encoder, "turn/completed", map[string]any{"threadId": "provider-thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "error": nil}})
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	writeRawNotification(encoder, "item/completed", map[string]any{
		"threadId": "provider-thread-1",
		"turnId":   "turn-1",
		"item": map[string]any{
			"type":  "agentMessage",
			"id":    "commentary-1",
			"text":  "working",
			"phase": "commentary",
		},
	})
	writeRawNotification(encoder, "item/completed", map[string]any{
		"threadId": "provider-thread-1",
		"turnId":   "turn-1",
		"item": map[string]any{
			"type":  "agentMessage",
			"id":    "final-1",
			"text":  "done",
			"phase": "final_answer",
		},
	})
	writeRawNotification(encoder, "turn/completed", map[string]any{
		"threadId": "provider-thread-1",
		"turn": map[string]any{
			"id":     "turn-1",
			"status": "completed",
			"error":  nil,
		},
	})

	_, _ = io.Copy(io.Discard, reader)
}

func runQueuedRawHelper(reader *bufio.Reader, encoder *json.Encoder) {
	for index, expectedText := range []string{"one", "two"} {
		request := readRawObject(reader)
		requireRaw(request["method"] == "turn/start")
		params, ok := request["params"].(map[string]any)
		requireRaw(ok && params["threadId"] == "provider-thread-1")
		input, ok := params["input"].([]any)
		requireRaw(ok && len(input) == 1)
		item, ok := input[0].(map[string]any)
		requireRaw(ok && item["type"] == "text" && item["text"] == expectedText)
		turnID := fmt.Sprintf("turn-%d", index+1)
		writeRawResult(encoder, request["id"], map[string]any{"turn": map[string]any{"id": turnID}})
		if index == 0 {
			time.Sleep(100 * time.Millisecond)
		}
		if index == 0 && os.Getenv("BRIA_RAW_HELPER_MODE") == "queue-failure" {
			writeRawNotification(encoder, "turn/completed", map[string]any{
				"threadId": "provider-thread-1", "turn": map[string]any{"id": turnID, "status": "failed", "error": map[string]any{"code": "provider"}},
			})
			continue
		}
		writeRawNotification(encoder, "item/completed", map[string]any{
			"threadId": "provider-thread-1", "turnId": turnID,
			"item": map[string]any{"type": "agentMessage", "id": "final-" + turnID, "text": "done-" + expectedText, "phase": "final_answer"},
		})
		writeRawNotification(encoder, "turn/completed", map[string]any{
			"threadId": "provider-thread-1", "turn": map[string]any{"id": turnID, "status": "completed", "error": nil},
		})
	}
	_, _ = io.Copy(io.Discard, reader)
}

func runQueuedInterruptRawHelper(reader *bufio.Reader, encoder *json.Encoder) {
	request := readRawObject(reader)
	requireRaw(request["method"] == "turn/start")
	writeRawResult(encoder, request["id"], map[string]any{"turn": map[string]any{"id": "turn-1"}})
	time.Sleep(100 * time.Millisecond)
	writeRawNotification(encoder, "item/completed", map[string]any{
		"threadId": "provider-thread-1", "turnId": "turn-1",
		"item": map[string]any{"type": "agentMessage", "id": "final-turn-1", "text": "done-one", "phase": "final_answer"},
	})
	writeRawNotification(encoder, "turn/completed", map[string]any{
		"threadId": "provider-thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "error": nil},
	})
	for index := 2; index <= 3; index++ {
		request = readRawObject(reader)
		requireRaw(request["method"] == "turn/start")
		turnID := fmt.Sprintf("turn-%d", index)
		writeRawResult(encoder, request["id"], map[string]any{"turn": map[string]any{"id": turnID}})
		request = readRawObject(reader)
		requireRaw(request["method"] == "turn/interrupt")
		params, ok := request["params"].(map[string]any)
		requireRaw(ok && params["threadId"] == "provider-thread-1" && params["turnId"] == turnID)
		writeRawResult(encoder, request["id"], map[string]any{})
		writeRawNotification(encoder, "turn/completed", map[string]any{
			"threadId": "provider-thread-1", "turn": map[string]any{"id": turnID, "status": "interrupted", "error": nil},
		})
	}
	_, _ = io.Copy(io.Discard, reader)
}

func TestAdapterInterruptsOnlyAcceptedRequestAndSuppressesFailedFinal(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=interrupt",
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"request-2","text":"wait"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "request-2",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"interrupt","request_id":"other-request"}`)
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"interrupt","request_id":"request-2"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "request-2",
		"status": "interrupted", "error_code": "interrupted",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not close after interrupted turn")
	}
}

func TestAdapterQueuesEarlyInterruptUntilAuthoritativeTurnID(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	defer adapterInput.Close()
	adapterOutput := newLineWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(ctx, parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=buffered-terminal",
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"request-early","text":"wait"}`)
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"interrupt","request_id":"request-early"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "request-early",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "request-early",
		"status": "interrupted", "error_code": "interrupted",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-runDone
		t.Fatal("early interrupt was not correlated after turn acceptance")
	}
}

func TestAdapterCloseCancelsActiveTurnAndWaitsForRawChild(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=stall",
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"request-3","text":"wait"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "request-3",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not cancel active turn and confirm child exit")
	}
}

func TestAdapterFailedTurnHasSanitizedCodeAndNoFinal(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=failed",
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"request-4","text":"fail"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "request-4",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "request-4",
		"status": "failed", "error_code": "provider_error",
	})
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not close after failed turn")
	}
}

func TestAdapterTerminatesRawChildAfterUnsupportedServerRequest(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	defer adapterInput.Close()
	adapterOutput := newLineWriter()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=server-request",
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"submit","request_id":"request-unsupported","text":"ask"}`)
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "accepted", "request_id": "request-unsupported",
	})
	assertJSONLine(t, adapterOutput, map[string]any{
		"protocol": float64(1), "type": "completed", "request_id": "request-unsupported",
		"status": "failed", "error_code": "protocol_error",
	})
	select {
	case err := <-runDone:
		if !errors.Is(err, codex.ErrAdapterProtocol) {
			t.Fatalf("RunAdapter() error = %v, want ErrAdapterProtocol", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() left raw child wedged after unsupported server request")
	}
}

func TestAdapterContextCancellationStopsAndWaitsForRawChild(t *testing.T) {
	workdir := t.TempDir()
	parentInput, adapterInput := io.Pipe()
	defer adapterInput.Close()
	adapterOutput := newLineWriter()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(ctx, parentInput, adapterOutput, codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=stall",
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunAdapter() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not stop and wait after context cancellation")
	}
}

func readyLine() map[string]any {
	return map[string]any{
		"protocol": float64(1), "type": "ready", "provider_session_id": "provider-thread-1",
		"readiness": "protocol", "authentication": "unknown",
	}
}

func readRawObject(reader *bufio.Reader) map[string]any {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		os.Exit(80)
	}
	var value map[string]any
	if json.Unmarshal(line, &value) != nil {
		os.Exit(81)
	}
	return value
}

func writeRawResult(encoder *json.Encoder, id any, result any) {
	if encoder.Encode(map[string]any{"id": id, "result": result}) != nil {
		os.Exit(82)
	}
}

func writeRawNotification(encoder *json.Encoder, method string, params any) {
	if encoder.Encode(map[string]any{"method": method, "params": params}) != nil {
		os.Exit(83)
	}
}

func requireRaw(ok bool) {
	if !ok {
		os.Exit(84)
	}
}

type lineWriter struct {
	lines chan []byte
}

func newLineWriter() *lineWriter {
	return &lineWriter{lines: make(chan []byte, 16)}
}

func (writer *lineWriter) Write(data []byte) (int, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			writer.lines <- []byte(line)
		}
	}
	return len(data), nil
}

func assertJSONLine(t *testing.T, writer *lineWriter, want map[string]any) {
	t.Helper()
	select {
	case line := <-writer.lines:
		var got map[string]any
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("decode adapter output %q: %v", line, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("adapter output = %#v, want %#v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for adapter output %#v", want)
	}
}

func writeParentLine(t *testing.T, writer *io.PipeWriter, line string) {
	t.Helper()
	if _, err := fmt.Fprintln(writer, line); err != nil {
		t.Fatalf("write adapter input: %v", err)
	}
}

func TestAdapterRejectsRelativeRawCommandWithoutStarting(t *testing.T) {
	tests := []struct {
		name    string
		command []string
	}{
		{name: "relative", command: []string{filepath.Join("relative", "codex"), "app-server"}},
		{name: "missing app server", command: []string{"/opt/codex", "exec"}},
		{name: "duplicate mode", command: []string{"/opt/codex", "app-server", "app-server"}},
		{name: "daemon", command: []string{"/opt/codex", "app-server", "daemon"}},
		{name: "proxy", command: []string{"/opt/codex", "proxy", "app-server"}},
		{name: "listen websocket", command: []string{"/opt/codex", "app-server", "--listen=ws://localhost"}},
		{name: "listen unix", command: []string{"/opt/codex", "app-server", "--listen", "unix:///tmp/socket"}},
		{name: "token", command: []string{"/opt/codex", "app-server", "--ws-token=value"}},
		{name: "secret", command: []string{"/opt/codex", "app-server", "--secret=value"}},
		{name: "ws auth", command: []string{"/opt/codex", "app-server", "--ws-auth=required"}},
		{name: "auth", command: []string{"/opt/codex", "app-server", "--auth=required"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, writer := io.Pipe()
			defer writer.Close()
			err := codex.RunAdapter(context.Background(), input, io.Discard, codex.AdapterConfig{
				RawCommand: test.command,
				Workdir:    t.TempDir(),
				ClientInfo: codex.ClientInfo{Name: "bria", Version: "test"},
			})
			if err == nil {
				t.Fatal("RunAdapter() error = nil, want unsafe argv rejected")
			}
			for _, argument := range test.command {
				if strings.Contains(err.Error(), argument) {
					t.Fatalf("RunAdapter() error leaks argv %q: %v", argument, err)
				}
			}
		})
	}
}
