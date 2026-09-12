package nativeadapter

import (
	"bytes"
	"context"
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

const (
	idleObserverStartupBound = 2 * time.Second
	idleScreenReactionBound  = 450 * time.Millisecond
)

func TestNativeAdapterHealthyObserverEliminatesIdleTmuxPolling(t *testing.T) {
	wrapper := installIdleEfficiencyTmuxWrapper(t, "")
	h, trigger := startIdleEfficiencyFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("Ready: %+v", ready)
	}

	observer := waitForTmuxInvocation(t, wrapper.log, idleObserverStartupBound, isReadOnlyObserverInvocation)
	if !containsOrderedArguments(observer, "-N", "-C", "attach-session", "-r", "-t", "cli:0.0") {
		t.Fatalf("tmux observer is not the exact read-only pane attach: %q", observer)
	}
	waitForObservationContaining(t, h, idleObserverStartupBound, "Claude Code")

	baseline := readTmuxInvocations(t, wrapper.log)
	assertNoNewPollingInvocation(t, wrapper.log, len(baseline), 550*time.Millisecond)

	triggerTerminalOutput(t, trigger)
	waitForObservationContaining(t, h, idleScreenReactionBound, "ASYNC-IDLE-OUTPUT")
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

func TestNativeAdapterObserverStartFailureFallsBackToPolling(t *testing.T) {
	wrapper := installIdleEfficiencyTmuxWrapper(t, "fail")
	h, trigger := startIdleEfficiencyFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("Ready after observer failure: %+v", ready)
	}
	waitForTmuxInvocation(t, wrapper.log, idleObserverStartupBound, isReadOnlyObserverInvocation)
	waitForTmuxInvocation(t, wrapper.log, idleObserverStartupBound, isPollingInvocation)

	triggerTerminalOutput(t, trigger)
	waitForObservationContaining(t, h, idleScreenReactionBound, "ASYNC-IDLE-OUTPUT")
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

func TestNativeAdapterObserverDisconnectFallsBackToPolling(t *testing.T) {
	wrapper := installIdleEfficiencyTmuxWrapper(t, "disconnect")
	h, trigger := startIdleEfficiencyFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("Ready after observer disconnect: %+v", ready)
	}
	waitForTmuxInvocation(t, wrapper.log, idleObserverStartupBound, isReadOnlyObserverInvocation)
	waitForTmuxInvocation(t, wrapper.log, idleObserverStartupBound, isPollingInvocation)

	triggerTerminalOutput(t, trigger)
	waitForObservationContaining(t, h, idleScreenReactionBound, "ASYNC-IDLE-OUTPUT")
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

func TestNativeAdapterObserverStallFallsBackToPolling(t *testing.T) {
	wrapper := installIdleEfficiencyTmuxWrapper(t, "stall")
	h, trigger := startIdleEfficiencyFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("Ready after observer stall: %+v", ready)
	}
	waitForTmuxInvocation(t, wrapper.log, idleObserverStartupBound, isReadOnlyObserverInvocation)
	waitForObservationContaining(t, h, idleObserverStartupBound, "Claude Code")
	baseline := len(readTmuxInvocations(t, wrapper.log))
	waitForNewTmuxInvocation(t, wrapper.log, baseline, idleObserverStartupBound, isPollingInvocation)

	triggerTerminalOutput(t, trigger)
	waitForObservationContaining(t, h, idleScreenReactionBound, "ASYNC-IDLE-OUTPUT")
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

func TestNativeAdapterCapturesOutputImmediatelyWhenObserverStalls(t *testing.T) {
	wrapper := installIdleEfficiencyTmuxWrapper(t, "late-stall")
	h, trigger := startIdleEfficiencyFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("Ready before late observer stall: %+v", ready)
	}
	waitForTmuxInvocation(t, wrapper.log, idleObserverStartupBound, isReadOnlyObserverInvocation)
	waitForObservationContaining(t, h, idleObserverStartupBound, "Claude Code")
	waitForPath(t, wrapper.phase, idleObserverStartupBound)

	triggerTerminalOutput(t, trigger)
	waitForObservationContaining(t, h, 300*time.Millisecond, "ASYNC-IDLE-OUTPUT")
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

func TestNativeAdapterActiveTurnRemainsBoundedAndOrderedWithIdleObserver(t *testing.T) {
	installIdleEfficiencyTmuxWrapper(t, "")
	h := startNativeFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("Ready: %+v", ready)
	}
	waitForObservationContaining(t, h, idleObserverStartupBound, "›")

	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "bounded-active", MessageID: "bounded-message", Text: "fixture prompt"})
	transcript := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "rollout-"+fixtureSession+".jsonl")
	waitForFileText(t, transcript, 2*time.Second, `"type":"task_complete"`)

	deadline := time.NewTimer(300 * time.Millisecond)
	defer deadline.Stop()
	want := []runtimeprotocol.MessageType{runtimeprotocol.TypeAccepted, runtimeprotocol.TypeFinal, runtimeprotocol.TypeCompleted}
	for index := 0; index < len(want); {
		select {
		case line := <-h.lines:
			message, err := runtimeprotocol.DecodeAdapterLine(line, runtimeprotocol.Limits{})
			if err != nil {
				t.Fatalf("decode active frame: %v", err)
			}
			if message.Type != want[index] || message.RequestID != "bounded-active" {
				t.Fatalf("active frame %d = %s request %q, want %s", index, message.Type, message.RequestID, want[index])
			}
			if message.Type == runtimeprotocol.TypeAccepted && message.MessageID != "bounded-message" {
				t.Fatal("active acceptance lost message identity")
			}
			if message.Type == runtimeprotocol.TypeFinal && message.Text != "fixture final" {
				t.Fatalf("active final = %q", message.Text)
			}
			index++
		case <-deadline.C:
			t.Fatalf("active transcript frames exceeded bounded reaction after durable completion; received %d of %d", index, len(want))
		case err := <-h.done:
			h.done <- err
			t.Fatalf("adapter exited during active turn: %v", err)
		}
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

func TestNativeAdapterHangingObserverDoesNotDelayFirstRequest(t *testing.T) {
	installIdleEfficiencyTmuxWrapper(t, "hang")
	h := startNativeFixture(t)
	if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
		t.Fatalf("Ready: %+v", ready)
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeSubmit, RequestID: "during-observer-start", MessageID: "first-message", Text: "fixture prompt"})

	transcript := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "rollout-"+fixtureSession+".jsonl")
	waitForFileText(t, transcript, 2*time.Second, `"type":"task_complete"`)
	deadline := time.NewTimer(300 * time.Millisecond)
	defer deadline.Stop()
	want := []runtimeprotocol.MessageType{runtimeprotocol.TypeAccepted, runtimeprotocol.TypeFinal, runtimeprotocol.TypeCompleted}
	for index := 0; index < len(want); {
		select {
		case line := <-h.lines:
			message, err := runtimeprotocol.DecodeAdapterLine(line, runtimeprotocol.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if message.Type == runtimeprotocol.TypeNativeObservation {
				continue
			}
			if message.Type != want[index] {
				t.Fatalf("frame %d = %s, want %s", index, message.Type, want[index])
			}
			index++
		case <-deadline.C:
			t.Fatalf("hanging observer delayed first request; received %d of %d frames", index, len(want))
		}
	}
	h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
}

type idleTmuxWrapper struct{ log, phase string }

func installIdleEfficiencyTmuxWrapper(t *testing.T, observerBehavior string) idleTmuxWrapper {
	t.Helper()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	logPath := filepath.Join(root, "tmux-invocations.log")
	failMarker := filepath.Join(root, "fail-observer")
	hangMarker := filepath.Join(root, "hang-observer")
	disconnectMarker := filepath.Join(root, "disconnect-observer")
	stallMarker := filepath.Join(root, "stall-observer")
	lateStallMarker := filepath.Join(root, "late-stall-observer")
	phaseMarker := filepath.Join(root, "late-stall-started")
	if observerBehavior == "fail" {
		if err := os.WriteFile(failMarker, []byte("fail\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if observerBehavior == "hang" {
		if err := os.WriteFile(hangMarker, []byte("hang\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if observerBehavior == "disconnect" {
		if err := os.WriteFile(disconnectMarker, []byte("disconnect\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if observerBehavior == "stall" {
		if err := os.WriteFile(stallMarker, []byte("stall\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if observerBehavior == "late-stall" {
		if err := os.WriteFile(lateStallMarker, []byte("late-stall\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
{
  for arg in "$@"; do printf '%%s\t' "$arg"; done
  printf '\n'
} >> %s
if [ -e %s ]; then
  for arg in "$@"; do
    if [ "$arg" = "-C" ]; then exit 73; fi
  done
fi
if [ -e %s ]; then
  for arg in "$@"; do
    if [ "$arg" = "-C" ]; then exec sleep 10; fi
  done
fi
if [ -e %s ]; then
  for arg in "$@"; do
    if [ "$arg" = "-C" ]; then
      printf '%%s\n' '%%begin 1 1 0' '%%end 1 1 0'
      exit 0
    fi
  done
fi
if [ -e %s ]; then
  for arg in "$@"; do
    if [ "$arg" = "-C" ]; then
      printf '%%s\n' '%%begin 1 1 0' '%%end 1 1 0'
      exec sleep 10
    fi
  done
fi
if [ -e %s ]; then
  for arg in "$@"; do
    if [ "$arg" = "-C" ]; then
      printf '%%s\n' '%%begin 1 1 0' '%%end 1 1 0'
      sequence=2
      count=0
      while IFS= read -r heartbeat; do
        count=$((count + 1))
        if [ "$count" -gt 5 ]; then exec sleep 10; fi
        printf '%%%%begin 1 %%s 1\n0\n%%%%end 1 %%s 1\n' "$sequence" "$sequence"
        sequence=$((sequence + 1))
        if [ "$count" -eq 5 ]; then : > %s; fi
      done
      exit 0
    fi
  done
fi
exec %s "$@"
`, shellQuote(logPath), shellQuote(failMarker), shellQuote(hangMarker), shellQuote(disconnectMarker), shellQuote(stallMarker), shellQuote(lateStallMarker), shellQuote(phaseMarker), shellQuote(realTmux))
	wrapperPath := filepath.Join(root, "tmux")
	if err := os.WriteFile(wrapperPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return idleTmuxWrapper{log: logPath, phase: phaseMarker}
}

func startIdleEfficiencyFixture(t *testing.T) (*adapterHarness, string) {
	t.Helper()
	root := t.TempDir()
	trigger := filepath.Join(root, "async-output.fifo")
	if output, err := exec.Command("mkfifo", trigger).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v: %s", err, bytes.TrimSpace(output))
	}
	fixture := filepath.Join(root, "idle-fixture")
	script := fmt.Sprintf(`#!/bin/sh
printf '\033[2J\033[HClaude Code\n❯\n'
(
  IFS= read -r signal < %s || exit 0
  printf '\033[2J\033[HClaude Code\nASYNC-IDLE-OUTPUT\n❯\n'
) &
watcher=$!
trap 'kill "$watcher" 2>/dev/null || true' EXIT HUP INT TERM
while IFS= read -r line; do :; done
`, shellQuote(trigger))
	if err := os.WriteFile(fixture, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	h := &adapterHarness{ctx: ctx, input: inWriter, lines: make(chan []byte, 64), done: make(chan error, 1), readerDone: make(chan struct{})}
	go h.scanOutput(outReader)
	go func() {
		err := Run(ctx, inReader, outWriter, Config{
			Provider: domain.ProviderClaude, Command: []string{fixture}, Workdir: root,
			ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ(),
		})
		_ = outWriter.Close()
		h.done <- err
	}()
	t.Cleanup(func() {
		cancel()
		_ = inWriter.Close()
		_ = outReader.Close()
		select {
		case <-h.done:
		case <-time.After(4 * time.Second):
			t.Error("idle efficiency fixture cleanup timed out")
		}
		<-h.readerDone
	})
	return h, trigger
}

func triggerTerminalOutput(t *testing.T, path string) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- os.WriteFile(path, []byte("emit\n"), 0o600) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("trigger asynchronous terminal output: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal output fixture did not accept trigger")
	}
}

func waitForObservationContaining(t *testing.T, h *adapterHarness, bound time.Duration, text string) {
	t.Helper()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	for {
		select {
		case line := <-h.lines:
			message, err := runtimeprotocol.DecodeAdapterLine(line, runtimeprotocol.Limits{})
			if err != nil {
				t.Fatalf("decode observation: %v", err)
			}
			if message.Type == runtimeprotocol.TypeNativeObservation && strings.Contains(message.Text, text) {
				return
			}
		case err := <-h.done:
			h.done <- err
			t.Fatalf("adapter exited before screen observation %q: %v", text, err)
		case <-timer.C:
			t.Fatalf("screen observation %q exceeded %s", text, bound)
		}
	}
}

func waitForTmuxInvocation(t *testing.T, path string, bound time.Duration, predicate func([]string) bool) []string {
	t.Helper()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, invocation := range readTmuxInvocations(t, path) {
			if predicate(invocation) {
				return invocation
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("expected tmux invocation was not observed within %s; got %q", bound, readTmuxInvocations(t, path))
		}
	}
}

func waitForNewTmuxInvocation(t *testing.T, path string, start int, bound time.Duration, predicate func([]string) bool) []string {
	t.Helper()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		invocations := readTmuxInvocations(t, path)
		if start > len(invocations) {
			t.Fatalf("tmux invocation log shrank from %d to %d", start, len(invocations))
		}
		for _, invocation := range invocations[start:] {
			if predicate(invocation) {
				return invocation
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("new tmux invocation was not observed within %s; got %q", bound, invocations[start:])
		}
	}
}

func assertNoNewPollingInvocation(t *testing.T, path string, start int, bound time.Duration) {
	t.Helper()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		invocations := readTmuxInvocations(t, path)
		if start > len(invocations) {
			t.Fatalf("tmux invocation log shrank from %d to %d", start, len(invocations))
		}
		for _, invocation := range invocations[start:] {
			if isPollingInvocation(invocation) {
				t.Fatalf("idle adapter launched a short tmux poll after observer attach: %q", invocation)
			}
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return
		}
	}
}

func waitForFileText(t *testing.T, path string, bound time.Duration, want string) {
	t.Helper()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte(want)) {
			return
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("%q was not written to %s within %s", want, path, bound)
		}
	}
}

func waitForPath(t *testing.T, path string, bound time.Duration) {
	t.Helper()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("path %s was not created within %s", path, bound)
		}
	}
}

func readTmuxInvocations(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	result := make([][]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(line, "\t"), "\t")
		result = append(result, fields)
	}
	return result
}

func isReadOnlyObserverInvocation(arguments []string) bool {
	return containsOrderedArguments(arguments, "-C", "attach-session", "-r")
}

func isPollingInvocation(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "capture-pane" || argument == "#{pane_dead}" {
			return true
		}
	}
	return false
}

func containsOrderedArguments(arguments []string, want ...string) bool {
	index := 0
	for _, argument := range arguments {
		if index < len(want) && argument == want[index] {
			index++
		}
	}
	return index == len(want)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
