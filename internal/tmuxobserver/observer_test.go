package tmuxobserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const helperModeEnvironment = "BRIA_TMUX_OBSERVER_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnvironment); mode != "" {
		runHelper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runHelper(mode string) {
	receipt := os.Getenv("BRIA_TMUX_OBSERVER_RECEIPT")
	encoded, _ := json.Marshal(os.Args[1:])
	if receipt == "" || os.WriteFile(receipt, encoded, 0o600) != nil {
		os.Exit(2)
	}
	if mode == "no-handshake" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	if mode == "foreign-end" {
		_, _ = io.WriteString(os.Stdout, "%end 9 9 0\n")
		writePhase()
		gate := os.Getenv("BRIA_TMUX_OBSERVER_GATE")
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		_, _ = io.WriteString(os.Stdout, "%begin 1 1 0\n%end 1 1 0\n")
		serveHeartbeats()
		return
	}
	if mode == "reject" {
		_, _ = io.WriteString(os.Stdout, "%begin 1 1 0\nprivate-payload-must-not-escape\n%error 1 1 0\n")
		return
	}
	_, _ = io.WriteString(os.Stdout, "%begin 1 1 0\n%end 1 1 0\n")
	switch mode {
	case "attach":
		serveHeartbeats()
	case "stall":
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "flood":
		for range 1024 {
			_, _ = io.WriteString(os.Stdout, "%output %1 private-payload\n")
		}
		_, _ = io.WriteString(os.Stdout, "%exit\n")
	case "exit":
		_, _ = io.WriteString(os.Stdout, "%exit finished\n")
	case "pane-exit":
		_, _ = io.WriteString(os.Stdout, "%pane-exited %1\n")
	case "disconnect":
		return
	case "oversized":
		_, _ = io.WriteString(os.Stdout, "%output %1 "+strings.Repeat("private-payload", maxControlLineBytes)+"\n")
	case "payload-exit":
		_, _ = io.WriteString(os.Stdout, "%output %1 private-payload-must-not-escape\n%exit\n")
	default:
		os.Exit(3)
	}
}

func serveHeartbeats() {
	scanner := bufio.NewScanner(os.Stdin)
	sequence := 2
	for scanner.Scan() {
		_, _ = fmt.Fprintf(os.Stdout, "%%begin 1 %d 1\n0\n%%end 1 %d 1\n", sequence, sequence)
		sequence++
	}
}

func writePhase() {
	if path := os.Getenv("BRIA_TMUX_OBSERVER_PHASE"); path != "" {
		_ = os.WriteFile(path, []byte("ready"), 0o600)
	}
}

func TestStartUsesReadOnlyControlModeAndPublishesInitialHint(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(t.TempDir(), "argv.json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observer, err := Start(ctx, Spec{
		Executable: executable,
		Socket:     "/private/socket",
		Target:     "cli:0.0",
		Environment: append(os.Environ(),
			helperModeEnvironment+"=attach",
			"BRIA_TMUX_OBSERVER_RECEIPT="+receipt,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-observer.Updates():
	case <-ctx.Done():
		t.Fatal("successful attach did not publish an initial update")
	}
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var arguments []string
	if err := json.Unmarshal(data, &arguments); err != nil {
		t.Fatal(err)
	}
	want := []string{"-u", "-N", "-C", "-S", "/private/socket", "attach-session", "-r", "-t", "cli:0.0"}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("argv=%v, want %v", arguments, want)
	}
}

func TestUpdatesCoalesceInitialAndOutputFlood(t *testing.T) {
	observer, cancel := startHelper(t, "flood", "")
	defer cancel()
	t.Cleanup(func() { _ = observer.Close() })
	if err := awaitDone(t, observer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observer.Updates():
	case <-time.After(time.Second):
		t.Fatal("coalesced update missing")
	}
	select {
	case _, ok := <-observer.Updates():
		if ok {
			t.Fatal("output flood was not coalesced")
		}
	default:
		t.Fatal("Updates did not close after terminal result")
	}
}

func TestAttachRequiresMatchingBeginAndEnd(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	receipt := filepath.Join(root, "argv.json")
	phase := filepath.Join(root, "foreign-end-written")
	gate := filepath.Join(root, "allow-real-handshake")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	environment := append(os.Environ(),
		helperModeEnvironment+"=foreign-end",
		"BRIA_TMUX_OBSERVER_RECEIPT="+receipt,
		"BRIA_TMUX_OBSERVER_PHASE="+phase,
		"BRIA_TMUX_OBSERVER_GATE="+gate,
	)
	type startResult struct {
		observer *Observer
		err      error
	}
	started := make(chan startResult, 1)
	go func() {
		observer, err := Start(ctx, Spec{Executable: executable, Socket: "/private/socket", Target: "cli:0.0", Environment: environment})
		started <- startResult{observer: observer, err: err}
	}()
	waitForFile(t, phase)
	select {
	case result := <-started:
		if result.observer != nil {
			_ = result.observer.Close()
		}
		t.Fatalf("foreign %%end completed attach: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(gate, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := <-started
	if result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(func() { _ = result.observer.Close() })
	select {
	case <-result.observer.Updates():
	case <-ctx.Done():
		t.Fatal("matching handshake did not publish initial hint")
	}
}

func TestNilEnvironmentInheritsParent(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv(helperModeEnvironment, "attach")
	t.Setenv("BRIA_TMUX_OBSERVER_RECEIPT", receipt)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observer, err := Start(ctx, Spec{Executable: executable, Socket: "/private/socket", Target: "cli:0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receipt); err != nil {
		t.Fatalf("nil Environment did not inherit parent variables: %v", err)
	}
}

func TestAttachRejectionDoesNotExposeControlPayload(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(t.TempDir(), "argv.json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observer, err := Start(ctx, Spec{
		Executable: executable, Socket: "/private/socket", Target: "cli:0.0",
		Environment: append(os.Environ(), helperModeEnvironment+"=reject", "BRIA_TMUX_OBSERVER_RECEIPT="+receipt),
	})
	if observer != nil {
		_ = observer.Close()
		t.Fatal("rejected attach returned an observer")
	}
	if !errors.Is(err, errAttachRejected) {
		t.Fatalf("attach error=%v, want rejection", err)
	}
	if strings.Contains(fmt.Sprint(err), "private-payload") {
		t.Fatalf("control payload escaped through attach error: %v", err)
	}
}

func TestStartBoundsMissingAttachHandshakeAndReapsClient(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(t.TempDir(), "argv.json")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	started := time.Now()
	observer, err := Start(ctx, Spec{
		Executable: executable, Socket: "/private/socket", Target: "cli:0.0",
		Environment: append(os.Environ(), helperModeEnvironment+"=no-handshake", "BRIA_TMUX_OBSERVER_RECEIPT="+receipt),
	})
	elapsed := time.Since(started)
	if observer != nil {
		_ = observer.Close()
		t.Fatal("missing handshake returned an observer")
	}
	if err == nil {
		t.Fatal("missing handshake succeeded")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start depended on caller deadline after %s", elapsed)
	}
	if elapsed < 4*time.Second {
		t.Fatalf("attach timeout fired prematurely: %s", elapsed)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("internal attach bound exceeded: %s", elapsed)
	}
}

func TestPostAttachHeartbeatStallTerminatesObserver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observer := startHelperContext(t, ctx, "stall", "")
	select {
	case <-observer.Updates():
	case <-ctx.Done():
		t.Fatal("initial attach hint missing")
	}
	started := time.Now()
	if err := awaitDone(t, observer); !errors.Is(err, errDisconnected) {
		t.Fatalf("stalled heartbeat result=%v, want disconnected", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("post-attach stall exceeded fallback bound: %s", elapsed)
	}
}

func TestTerminalEventsAndDisconnectHaveSingleResult(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr error
	}{
		{name: "clean control exit", mode: "exit"},
		{name: "pane exit", mode: "pane-exit"},
		{name: "unexpected disconnect", mode: "disconnect", wantErr: errDisconnected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer, cancel := startHelper(t, test.mode, "")
			defer cancel()
			err := awaitDone(t, observer)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Done error=%v, want %v", err, test.wantErr)
			}
			if err := observer.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContextCancellationTerminatesAndReapsClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	observer := startHelperContext(t, ctx, "attach", "")
	cancel()
	if err := awaitDone(t, observer); !errors.Is(err, context.Canceled) {
		t.Fatalf("Done error=%v, want context cancellation", err)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	if observer.command.ProcessState == nil {
		t.Fatal("Close returned before exact client was reaped")
	}
}

func TestCloseIsConcurrentIdempotentAndReapsClient(t *testing.T) {
	observer, cancel := startHelper(t, "attach", "")
	defer cancel()
	const callers = 16
	errorsSeen := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- observer.Close()
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := awaitDone(t, observer); err != nil {
		t.Fatalf("Close terminal result=%v", err)
	}
	if observer.command.ProcessState == nil {
		t.Fatal("Close returned before exact client was reaped")
	}
}

func TestOversizedControlLineFailsClosedWithoutPayload(t *testing.T) {
	observer, cancel := startHelper(t, "oversized", "")
	defer cancel()
	err := awaitDone(t, observer)
	if !errors.Is(err, errProtocolRead) {
		t.Fatalf("Done error=%v, want bounded protocol failure", err)
	}
	if strings.Contains(fmt.Sprint(err), "private-payload") {
		t.Fatalf("terminal payload escaped through error: %v", err)
	}
}

func TestOutputExposesOnlyHintAndNoPayload(t *testing.T) {
	observer, cancel := startHelper(t, "payload-exit", "")
	defer cancel()
	select {
	case hint, ok := <-observer.Updates():
		if !ok || hint != (struct{}{}) {
			t.Fatal("output did not produce an opaque hint")
		}
	case <-time.After(time.Second):
		t.Fatal("output hint missing")
	}
	if err := awaitDone(t, observer); err != nil {
		t.Fatalf("clean exit error contains terminal state: %v", err)
	}
}

func awaitDone(t *testing.T, observer *Observer) error {
	t.Helper()
	select {
	case err, ok := <-observer.Done():
		if !ok {
			t.Fatal("Done closed without its terminal result")
		}
		if _, ok := <-observer.Done(); ok {
			t.Fatal("Done published more than one terminal result")
		}
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not finish")
		return fmt.Errorf("unreachable")
	}
}

func startHelper(t *testing.T, mode, phase string) (*Observer, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	return startHelperContext(t, ctx, mode, phase), cancel
}

func startHelperContext(t *testing.T, ctx context.Context, mode, phase string) *Observer {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(t.TempDir(), "argv.json")
	environment := append(os.Environ(), helperModeEnvironment+"="+mode, "BRIA_TMUX_OBSERVER_RECEIPT="+receipt)
	if phase != "" {
		environment = append(environment, "BRIA_TMUX_OBSERVER_PHASE="+phase)
	}
	observer, err := Start(ctx, Spec{Executable: executable, Socket: "/private/socket", Target: "cli:0.0", Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	return observer
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("helper phase was not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
