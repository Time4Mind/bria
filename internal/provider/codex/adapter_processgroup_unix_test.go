//go:build darwin || linux

package codex_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"bria/internal/provider/codex"
)

func TestAdapterCancellationKillsRawProviderGrandchild(t *testing.T) {
	workdir := t.TempDir()
	pidPath := workdir + "/grandchild.pid"
	readyPath := workdir + "/grandchild.ready"
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
				"BRIA_RAW_HELPER_MODE=grandchild",
				"BRIA_RAW_GRANDCHILD_PID_PATH=" + pidPath,
				"BRIA_RAW_GRANDCHILD_READY_PATH=" + readyPath,
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	assertJSONLine(t, adapterOutput, readyLine())
	rawPID, pid := waitForProviderPIDs(t, pidPath)
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	// Cmd.Start only proves that fork succeeded; the child can still fail
	// before exec or before entering the fixture body. Wait for a marker written
	// by the grandchild itself before asserting its process-group membership.
	waitForPath(t, readyPath)
	rawGroup, err := syscall.Getpgid(rawPID)
	if err != nil || rawGroup != rawPID {
		t.Fatalf("raw provider process group = %d, error = %v, want dedicated group %d", rawGroup, err, rawPID)
	}
	grandchildGroup, err := syscall.Getpgid(pid)
	if err != nil || grandchildGroup != rawPID {
		t.Fatalf("raw grandchild process group = %d, error = %v, want %d", grandchildGroup, err, rawPID)
	}

	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunAdapter() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not stop after cancellation")
	}

	deadline := time.Now().Add(2 * time.Second)
	for processExists(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("raw provider grandchild pid %d survived adapter cancellation", pid)
	}
}

func TestAdapterKillsRawGrandchildAfterLeaderAlreadyExited(t *testing.T) {
	workdir := t.TempDir()
	pidPath := workdir + "/leader-exit.pid"
	exitPath := workdir + "/leader-exiting"
	readyPath := workdir + "/leader-grandchild.ready"
	parentInput, adapterInput := io.Pipe()
	defer adapterInput.Close()
	runDone := make(chan error, 1)
	go func() {
		runDone <- codex.RunAdapter(context.Background(), parentInput, newLineWriter(), codex.AdapterConfig{
			RawCommand: []string{os.Args[0], "-test.run=^TestRawCodexAppServerHelperProcess$", "--", "app-server"},
			RawEnv: []string{
				"BRIA_CODEX_RAW_HELPER=1",
				"BRIA_EXPECT_WORKDIR=" + workdir,
				"BRIA_RAW_HELPER_MODE=leader-exit",
				"BRIA_RAW_GRANDCHILD_PID_PATH=" + pidPath,
				"BRIA_RAW_GRANDCHILD_READY_PATH=" + readyPath,
				"BRIA_RAW_LEADER_EXIT_PATH=" + exitPath,
			},
			Workdir:    workdir,
			ClientInfo: codex.ClientInfo{Name: "bria-test", Version: "0.1.0"},
		})
	}()
	_, childPID := waitForProviderPIDs(t, pidPath)
	defer func() { _ = syscall.Kill(childPID, syscall.SIGKILL) }()
	waitForPath(t, readyPath)
	waitForPath(t, exitPath)
	time.Sleep(25 * time.Millisecond)
	writeParentLine(t, adapterInput, `{"protocol":1,"type":"close"}`)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunAdapter() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAdapter() did not stop after raw leader exit")
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(childPID) {
		t.Fatalf("raw grandchild pid %d survived after its leader exited", childPID)
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("fixture path %q did not appear", path)
}

func waitForProviderPIDs(t *testing.T, path string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(encoded))
			if len(fields) == 2 {
				rawPID, rawErr := strconv.Atoi(fields[0])
				pid, childErr := strconv.Atoi(fields[1])
				if rawErr == nil && childErr == nil && rawPID > 1 && pid > 1 {
					return rawPID, pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("raw provider grandchild pid was not published")
	return 0, 0
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
