package nativeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
)

const fixtureSession = "11111111-2222-4333-8444-555555555555"

func TestMain(m *testing.M) {
	if os.Getenv("NATIVE_ADAPTER_BRIDGE") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if os.Getenv("NATIVE_ADAPTER_CANCEL_FD") == "3" {
			cancelFile := os.NewFile(3, "native-adapter-cancel")
			defer cancelFile.Close()
			go func() {
				var signal [1]byte
				_, _ = cancelFile.Read(signal[:])
				cancel()
			}()
		}
		workdir, _ := os.Getwd()
		env := append(os.Environ(), "NATIVE_ADAPTER_FIXTURE=1", "NATIVE_ADAPTER_BRIDGE=0")
		err := Run(ctx, os.Stdin, os.Stdout, Config{Provider: domain.ProviderCodex, Command: []string{os.Args[0]}, Workdir: workdir, ResumeID: fixtureSession, StateDir: filepath.Join(workdir, "receipts"), Environment: env})
		if err != nil {
			os.Exit(86)
		}
		os.Exit(0)
	}
	if os.Getenv("NATIVE_ADAPTER_FIXTURE") == "1" {
		runNativeFixture()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The fixture is a real terminal program, but never invokes a provider or a
// network. Only its append-only native transcript can prove turn acceptance.
func runNativeFixture() {
	workdir, err := os.Getwd()
	if err != nil {
		os.Exit(80)
	}
	root := filepath.Join(os.Getenv("CODEX_HOME"), "sessions")
	if os.MkdirAll(root, 0700) != nil {
		os.Exit(81)
	}
	file, err := os.OpenFile(filepath.Join(root, "rollout-"+fixtureSession+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(82)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	write := func(kind string, payload map[string]any) {
		if encoder.Encode(map[string]any{"type": kind, "payload": payload}) != nil {
			os.Exit(83)
		}
		if file.Sync() != nil {
			os.Exit(84)
		}
	}
	write("session_meta", map[string]any{"id": fixtureSession, "cwd": workdir})
	index, _ := json.Marshal(map[string]string{"id": fixtureSession, "thread_name": "CLI title"})
	if os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "session_index.jsonl"), append(index, '\n'), 0600) != nil {
		os.Exit(85)
	}
	fmt.Print("\x1b[2J\x1b[H›\n")
	scanner := bufio.NewScanner(os.Stdin)
	approvalPending := false
	for scanner.Scan() {
		text := strings.TrimSpace(strings.TrimLeft(scanner.Text(), "\x1b"))
		if approvalPending && text == "" {
			approvalPending = false
			fmt.Print("\x1b[2J\x1b[H✔ You approved codex to run fixture this time\nfixture result\n›\n")
			continue
		}
		switch text {
		case "/status":
			fmt.Printf("\x1b[2J\x1b[HSession: %s\nModel: gpt-fixture\n›\n", fixtureSession)
		case "/model":
			fmt.Print("\x1b[2J\x1b[HSelect a model\nModel: gpt-fixture\nEnter to select · Esc to cancel\n")
		case "/approval-full", "/approval-collapsed":
			approvalPending = true
			preview := "Environment: local\nReason: bounded fixture\n$ fixture\n"
			if text == "/approval-collapsed" {
				preview = "[… 1000 lines] ctrl + a view all\n"
			}
			fmt.Print("\x1b[2J\x1b[HWould you like to run the following command?\n\n" + preview + "\n› 1. Yes, proceed (y)\n2. No, and tell Codex what to do differently (esc)\n\nPress enter to confirm or esc to cancel\n")
		case "screen-only":
			fmt.Print("\x1b[2J\x1b[HFinished on screen, no transcript receipt\n›\n")
		case "async-picker":
			write("turn_context", map[string]any{"turn_id": "turn-async", "model": "gpt-fixture"})
			write("event_msg", map[string]any{"type": "user_message", "message": text})
			fmt.Print("\x1b[2J\x1b[HSelect a model\nModel: gpt-fixture\nEnter to select · Esc to cancel\n")
		default:
			write("turn_context", map[string]any{"turn_id": "turn-fixture", "model": "gpt-fixture"})
			write("event_msg", map[string]any{"type": "user_message", "message": text})
			write("event_msg", map[string]any{"type": "agent_message", "message": "fixture final", "phase": "final_answer"})
			write("event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-fixture"})
			fmt.Print("\x1b[2J\x1b[H›\n")
		}
	}
}

type adapterHarness struct {
	ctx        context.Context
	input      *io.PipeWriter
	lines      chan []byte
	done       chan error
	readerDone chan struct{}
	readerErr  error
}

func startNativeFixture(t *testing.T) *adapterHarness {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	private := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(private, "codex"))
	t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	h := &adapterHarness{ctx: ctx, input: inWriter, lines: make(chan []byte, 32), done: make(chan error, 1), readerDone: make(chan struct{})}
	go h.scanOutput(outReader)
	go func() {
		err := Run(ctx, inReader, outWriter, Config{Provider: domain.ProviderCodex, Command: []string{os.Args[0]}, Workdir: private, ResumeID: fixtureSession, StateDir: filepath.Join(private, "state"), Environment: os.Environ()})
		_ = outWriter.Close()
		h.done <- err
	}()
	t.Cleanup(func() {
		cancel()
		_ = inWriter.Close()
		_ = outReader.Close()
		select {
		case <-h.done:
		case <-time.After(6 * time.Second):
			t.Error("native fixture cleanup did not finish")
		}
		<-h.readerDone
	})
	return h
}

func (h *adapterHarness) scanOutput(reader io.Reader) {
	defer close(h.readerDone)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		select {
		case h.lines <- append([]byte(nil), scanner.Bytes()...):
		case <-h.ctx.Done():
			return
		}
	}
	h.readerErr = scanner.Err()
}

func (h *adapterHarness) send(t *testing.T, m runtimeprotocol.ParentMessage) {
	t.Helper()
	m.Protocol = 1
	line, err := runtimeprotocol.EncodeParentLine(m, runtimeprotocol.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := h.input.Write(line); result <- err }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal("fixture input timed out")
	}
}
func (h *adapterHarness) receive(t *testing.T) runtimeprotocol.AdapterMessage {
	t.Helper()
	for {
		message := h.receiveRaw(t)
		if message.Type != runtimeprotocol.TypeNativeObservation {
			return message
		}
	}
}
func (h *adapterHarness) receiveRaw(t *testing.T) runtimeprotocol.AdapterMessage {
	t.Helper()
	decode := func(line []byte) runtimeprotocol.AdapterMessage {
		message, err := runtimeprotocol.DecodeAdapterLine(line, runtimeprotocol.Limits{})
		if err != nil {
			t.Fatalf("invalid adapter frame (%d bytes): %v", len(line), err)
		}
		return message
	}
	select {
	case line := <-h.lines:
		return decode(line)
	case err := <-h.done:
		h.done <- err
		// Run can exit after writing a frame but before the scanner publishes
		// it. Drain output through scanner completion before declaring EOF.
		for {
			select {
			case line := <-h.lines:
				return decode(line)
			case <-h.readerDone:
				select {
				case line := <-h.lines:
					return decode(line)
				default:
					t.Fatalf("adapter exited before expected frame: %v (scanner: %v)", err, h.readerErr)
				}
			case <-h.ctx.Done():
				t.Fatal("adapter output drain timeout")
			}
		}
	case <-h.ctx.Done():
		t.Fatal("adapter frame timeout")
	}
	return runtimeprotocol.AdapterMessage{}
}

func TestNativeAdapterTerminalControlAndTranscriptReceipts(t *testing.T) {
	h := startNativeFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady || ready.ProviderSessionID != fixtureSession {
		t.Fatalf("ready identity/type mismatch: %s", ready.Type)
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "n-1", Command: "/model"})
	snapshot := h.receive(t)
	if snapshot.Type != runtimeprotocol.TypeNativeSnapshot || snapshot.Model != "gpt-fixture" || !snapshot.Interactive || !strings.Contains(snapshot.Text, "Select a model") || len(snapshot.Hash) != 64 {
		t.Fatal("native screen snapshot did not preserve CLI model/menu/hash")
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "n-stale", Key: "enter", ExpectedHash: strings.Repeat("0", 64)})
	stale := h.receive(t)
	if stale.Type != runtimeprotocol.TypeNativeSnapshot || stale.ErrorCode != "stale" || stale.Text != snapshot.Text {
		t.Fatal("stale key changed screen or lacked explicit stale result")
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "r-1", MessageID: "message-1", Text: "fixture prompt"})
	for _, kind := range []runtimeprotocol.MessageType{runtimeprotocol.TypeAccepted, runtimeprotocol.TypeFinal, runtimeprotocol.TypeCompleted} {
		message := h.receive(t)
		if message.Type != kind || message.RequestID != "r-1" {
			t.Fatalf("turn frame = %s request %s, want %s", message.Type, message.RequestID, kind)
		}
		if kind == runtimeprotocol.TypeAccepted && message.MessageID != "message-1" {
			t.Fatal("acceptance lost durable message identity")
		}
		if kind == runtimeprotocol.TypeFinal && message.Text != "fixture final" {
			t.Fatal("final not sourced from transcript")
		}
		if kind == runtimeprotocol.TypeCompleted && message.ProviderSessionName != "CLI title" {
			t.Fatal("completion dropped provider native title")
		}
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeReconcileAcceptedTurns, RequestID: "reconcile-1"})
	accepted := h.receive(t)
	if accepted.Type != runtimeprotocol.TypeAcceptedTurn || accepted.MessageID != "message-1" || accepted.Status != "completed" {
		t.Fatal("durable receipt reconciliation mismatch")
	}
	if h.receive(t).Type != runtimeprotocol.TypeReconciliationCompleted {
		t.Fatal("missing reconciliation terminal")
	}
	// A CLI showing an idle prompt/final-looking text is not acceptance proof.
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "r-screen", MessageID: "screen-message", Text: "screen-only"})
	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
wait:
	for {
		select {
		case line := <-h.lines:
			message, err := runtimeprotocol.DecodeAdapterLine(line, runtimeprotocol.Limits{})
			if err != nil || message.Type != runtimeprotocol.TypeNativeObservation {
				t.Fatal("screen-only output produced a turn receipt")
			}
		case <-deadline.C:
			break wait
		}
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

func TestNativeAdapterObservesPickerDuringActiveTurnWithoutControlCommand(t *testing.T) {
	h := startNativeFixture(t)
	if h.receive(t).Type != runtimeprotocol.TypeReady {
		t.Fatal("missing ready")
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "r-async", MessageID: "async", Text: "async-picker"})
	accepted, observed := false, false
	for i := 0; i < 16 && (!accepted || !observed); i++ {
		message := h.receiveRaw(t)
		switch message.Type {
		case runtimeprotocol.TypeAccepted:
			accepted = true
		case runtimeprotocol.TypeNativeObservation:
			if message.Interactive && strings.Contains(message.Text, "Select a model") && message.ProviderSessionID == fixtureSession && message.RequestID == "" {
				observed = true
			}
		default:
			t.Fatalf("unexpected turn/UI frame: %s", message.Type)
		}
	}
	if !accepted || !observed {
		t.Fatal("active-turn native picker was not observed")
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}
