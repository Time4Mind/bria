package nativeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/nativeterminal"
	"bria/internal/runtimeprotocol"
)

func startPersistentAdapterFixture(t *testing.T, config Config, parents ...context.Context) *adapterHarness {
	t.Helper()
	parent := context.Background()
	if len(parents) != 0 {
		parent = parents[0]
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	input, writer := io.Pipe()
	output, sink := io.Pipe()
	h := &adapterHarness{ctx: ctx, input: writer, lines: make(chan []byte, 64), done: make(chan error, 1), readerDone: make(chan struct{})}
	go h.scanOutput(output)
	go func() { err := Run(ctx, input, sink, config); _ = sink.Close(); h.done <- err }()
	t.Cleanup(func() {
		cancel()
		_ = writer.Close()
		_ = output.Close()
		select {
		case <-h.done:
		case <-time.After(6 * time.Second):
			t.Error("persistent adapter cleanup timed out")
			return // Never compete with an observer that has not joined.
		}
		<-h.readerDone
		if !config.Persistent {
			return
		}
		closeCtx, stopClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopClose()
		terminal, err := nativeterminal.AttachExisting(closeCtx, config.StateDir, nativeterminal.Binding{
			LogicalSessionID: config.LogicalSessionID, NativeSessionID: config.ResumeID, Provider: string(config.Provider), Workdir: config.Workdir})
		if errors.Is(err, nativeterminal.ErrNotManaged) {
			return // The test already physically closed its fixture.
		}
		if err != nil {
			t.Errorf("cannot prove persistent fixture ownership for cleanup: %v", err)
			return
		}
		if err := terminal.Close(closeCtx); err != nil {
			t.Errorf("persistent fixture physical cleanup failed: %v", err)
		}
	})
	return h
}

func TestAttachOnlyReadyDoesNotReadOversizedExistingTranscript(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
	config := Config{Persistent: true, LogicalSessionID: "logical-oversized", Provider: domain.ProviderCodex,
		Command: []string{os.Args[0]}, Workdir: root, ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ()}
	first := startPersistentAdapterFixture(t, config)
	if ready := first.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("first Ready: %+v", ready)
	}
	stopPersistentAdapterEOF(t, first)
	binding := nativeterminal.Binding{LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "codex", Workdir: root}
	defer func() {
		if term, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding); err == nil {
			_ = term.Close(context.Background())
		}
	}()
	path := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "rollout-"+fixtureSession+".jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(file, `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":"`+strings.Repeat("x", 2<<20)+`"}}`+"\n")
	if err := errors.Join(err, file.Close()); err != nil {
		t.Fatal(err)
	}
	config.AttachOnly, config.Command = true, nil
	second := startPersistentAdapterFixture(t, config)
	if ready := second.receive(t); ready.Type != runtimeprotocol.TypeReady || ready.ProviderSessionID != fixtureSession {
		t.Fatalf("attach read transcript before Ready: %+v", ready)
	}
	second.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
	if closed := second.receive(t); closed.Type != runtimeprotocol.TypeClosed || closed.ProviderSessionID != fixtureSession {
		t.Fatalf("physical Close ack missing: %+v", closed)
	}
	stopPersistentAdapterEOF(t, second)
}

func stopPersistentAdapterEOF(t *testing.T, h *adapterHarness) {
	t.Helper()
	_ = h.input.Close()
	select {
	case err := <-h.done:
		h.done <- err
		if err != nil {
			t.Fatalf("EOF detach failed: %v", err)
		}
	case <-h.ctx.Done():
		t.Fatal("EOF did not stop observer")
	}
}

func TestPersistentHarnessCleanupClosesAfterLaterCleanupFindsOwner(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
	config := Config{Persistent: true, LogicalSessionID: "logical-cleanup", Provider: domain.ProviderCodex,
		Command: []string{os.Args[0]}, Workdir: root, ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ()}
	binding := nativeterminal.Binding{LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "codex", Workdir: root}
	var record struct{ Socket string }
	t.Run("leave-observer-running", func(t *testing.T) {
		h := startPersistentAdapterFixture(t, config)
		if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
			t.Fatalf("Ready: %+v", ready)
		}
		paths, err := filepath.Glob(filepath.Join(config.StateDir, "terminal-bindings", "*.json"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("fixture binding: %v %v", paths, err)
		}
		data, err := os.ReadFile(paths[0])
		if err != nil || json.Unmarshal(data, &record) != nil || record.Socket == "" {
			t.Fatalf("fixture socket proof unavailable: %v", err)
		}
		// A Fatal also runs this later cleanup before the harness cleanup.
		// It cannot acquire the binding while the observer still owns it.
		t.Cleanup(func() {
			term, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding)
			if term != nil {
				_ = term.Detach(context.Background())
			}
			if !errors.Is(err, nativeterminal.ErrAlreadyAttached) {
				t.Errorf("fixture ownership was not held before harness cleanup: %v", err)
			}
		})
	})
	term, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding)
	if term != nil {
		_ = term.Close(context.Background()) // Contain the intentionally red regression.
	}
	if !errors.Is(err, nativeterminal.ErrNotManaged) {
		t.Fatalf("harness cleanup left an attachable fixture: %v", err)
	}
	if _, err := os.Lstat(record.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("harness cleanup left fixture socket: %v", err)
	}
}

func TestPersistentAdapterEOFAndAttachOnlyObserveAcceptedWithoutReplay(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
	config := Config{Persistent: true, LogicalSessionID: "logical-live", Provider: domain.ProviderCodex,
		Command: []string{os.Args[0]}, Workdir: root, ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ()}
	binding := nativeterminal.Binding{LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "codex", Workdir: root}
	defer func() {
		if term, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding); err == nil {
			_ = term.Close(context.Background())
		}
	}()
	first := startPersistentAdapterFixture(t, config)
	if ready := first.receive(t); ready.Type != runtimeprotocol.TypeReady || ready.ProviderSessionID != fixtureSession {
		t.Fatalf("first Ready: %+v", ready)
	}
	first.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "request", MessageID: "input", Text: "async-picker"})
	if accepted := first.receive(t); accepted.Type != runtimeprotocol.TypeAccepted {
		t.Fatalf("accepted: %+v", accepted)
	}
	stopPersistentAdapterEOF(t, first)
	terminal, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding)
	if err != nil {
		t.Fatalf("EOF destroyed exact accepted CLI: %v", err)
	}
	before, err := terminal.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := terminal.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	config.AttachOnly = true
	config.Command = []string{"/does/not/exist/must-not-launch"}
	second := startPersistentAdapterFixture(t, config)
	if ready := second.receive(t); ready.Type != runtimeprotocol.TypeReady || ready.ProviderSessionID != fixtureSession {
		t.Fatalf("attach Ready: %+v", ready)
	}
	second.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "screen"})
	if screen := second.receive(t); screen.Type != runtimeprotocol.TypeNativeSnapshot || screen.FullText != before {
		t.Fatalf("attach performed startup input: %+v", screen)
	}
	second.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeObserveAccepted, RequestID: "observe", MessageID: "input"})
	if accepted := second.receive(t); accepted.Type != runtimeprotocol.TypeAccepted {
		t.Fatalf("observe accepted: %+v", accepted)
	}
	path := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "rollout-"+fixtureSession+".jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, payload := range []map[string]any{
		{"type": "agent_message", "phase": "final", "message": "final after observer restart", "turn_id": "turn-async"},
		{"type": "task_complete", "turn_id": "turn-async"},
	} {
		if err := encoder.Encode(map[string]any{"type": "event_msg", "payload": payload}); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if final := second.receive(t); final.Type != runtimeprotocol.TypeFinal || final.RequestID != "observe" || final.Text != "final after observer restart" {
		t.Fatalf("continued final: %+v", final)
	}
	if completed := second.receive(t); completed.Type != runtimeprotocol.TypeCompleted {
		t.Fatalf("continued completion: %+v", completed)
	}
	transcript, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(transcript)
	users := 0
	for {
		var record struct{ Payload struct{ Type string } }
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if record.Payload.Type == "user_message" {
			users++
		}
	}
	_ = transcript.Close()
	if users != 1 {
		t.Fatalf("startup/reattach replayed provider input: user records=%d", users)
	}
	second.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
	if closed := second.receive(t); closed.Type != runtimeprotocol.TypeClosed || closed.ProviderSessionID != fixtureSession {
		t.Fatalf("physical Close ack missing: %+v", closed)
	}
	stopPersistentAdapterEOF(t, second)
	if terminal, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding); !errors.Is(err, nativeterminal.ErrTerminalUnavailable) {
		if terminal != nil {
			_ = terminal.Close(context.Background())
		}
		t.Fatalf("explicit Close left live CLI: %v", err)
	}
}
