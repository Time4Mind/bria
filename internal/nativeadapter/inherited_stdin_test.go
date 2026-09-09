package nativeadapter

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/runtimeprotocol"
)

func TestInheritedStdinCloseExitsWhileParentPipeStaysOpen(t *testing.T) {
	testInheritedStdin(t, false)
}

func TestInheritedStdinCancellationJoinsReaderWhileParentPipeStaysOpen(t *testing.T) {
	testInheritedStdin(t, true)
}

func testInheritedStdin(t *testing.T, cancelOnly bool) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	// Unix-domain sockets have a short path limit; keep the owned fixture root
	// independent of the descriptive Go test name.
	dir, err := os.MkdirTemp("/tmp", "native-input-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NATIVE_ADAPTER_BRIDGE=1", "CODEX_HOME="+filepath.Join(dir, "codex"), "TMPDIR="+dir)
	var cancelReader, cancelWriter *os.File
	if cancelOnly {
		cancelReader, cancelWriter, err = os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.ExtraFiles = []*os.File{cancelReader}
		cmd.Env = append(cmd.Env, "NATIVE_ADAPTER_CANCEL_FD=3")
		defer cancelReader.Close()
		defer cancelWriter.Close()
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if cancelReader != nil {
		if err := cancelReader.Close(); err != nil {
			t.Fatal(err)
		}
		cancelReader = nil
	}
	defer input.Close()
	defer output.Close()
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() {
		t.Fatal("subprocess readiness missing")
	}
	ready, err := runtimeprotocol.DecodeAdapterLine(scanner.Bytes(), runtimeprotocol.Limits{})
	if err != nil || ready.Type != runtimeprotocol.TypeReady {
		t.Fatal("subprocess readiness invalid")
	}
	line, err := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{Protocol: 1, Type: runtimeprotocol.TypeClose}, runtimeprotocol.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if cancelOnly {
		if _, err := cancelWriter.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := input.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil && !cancelOnly {
			t.Fatal("subprocess close failed")
		}
		if cancelOnly && cmd.ProcessState.ExitCode() != 86 {
			t.Fatal("cancelled adapter did not return classified failure")
		}
	case <-time.After(1500 * time.Millisecond):
		// Closing the writer here is test cleanup, not part of the asserted flow.
		_ = input.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		t.Fatal("close waited for parent stdin EOF instead of exiting")
	}
	sockets, err := filepath.Glob(filepath.Join(dir, "bria-terminal-*"))
	if err != nil || len(sockets) != 0 {
		t.Fatal("adapter left owned terminal/socket state")
	}
}
