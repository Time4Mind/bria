//go:build darwin || linux

package parakeet_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"bria/internal/speech/parakeet"
)

func TestCancellationTerminatesParakeetWrapperDescendants(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "child.pid")
	wrapper := filepath.Join(root, "wrapper")
	script := fmt.Sprintf("#!/bin/sh\n/bin/sleep 300 &\nprintf '%%s\\n' \"$!\" > %s\n\nwait\n", shellLiteral(pidPath))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(root, "model")
	audio := filepath.Join(root, "audio.ogg")
	for _, filename := range []string{model, audio} {
		if err := os.WriteFile(filename, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := parakeet.Command{
		Executable: wrapper, ModelPath: model, Arguments: []string{parakeet.ModelPathPlaceholder},
		Environment: []string{}, MaxTranscriptBytes: 1024, MaxDiagnosticBytes: 1024,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := command.Transcribe(ctx, audio)
		done <- err
	}()
	childPID := waitForChildPID(t, pidPath)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Transcribe error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Transcribe did not return after cancellation")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
			t.Fatalf("Parakeet descendant %d survived cancellation: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForChildPID(t *testing.T, filename string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		content, err := os.ReadFile(filename)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
			if parseErr == nil && pid > 1 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("child PID was not published: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
