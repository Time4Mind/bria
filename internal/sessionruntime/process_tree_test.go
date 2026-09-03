//go:build darwin || linux

package sessionruntime_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

const grandchildHelperEnvironment = "BRIA_SESSIONRUNTIME_GRANDCHILD"

const (
	treeAdapterEnvironment = "BRIA_SESSIONRUNTIME_TREE_ADAPTER"
	rawHelperEnvironment   = "BRIA_SESSIONRUNTIME_RAW_HELPER"
)

func TestTermAwareAdapterCleansSeparateRawProcessGroup(t *testing.T) {
	t.Parallel()
	starter := newTreeStarter(t, "cleanup", sessionruntime.Options{
		GracefulCloseTimeout:     30 * time.Millisecond,
		GracefulTerminateTimeout: time.Second,
	})
	request := testRequest(t.TempDir(), "term-cleanup")
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	rawPID := waitGrandchildPID(t, filepath.Join(request.Workdir, "raw.pid"))
	grandchildPID := waitGrandchildPID(t, filepath.Join(request.Workdir, "raw-grandchild.pid"))
	t.Cleanup(func() {
		_ = syscall.Kill(-rawPID, syscall.SIGKILL)
	})

	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	waitPIDGone(t, rawPID)
	waitPIDGone(t, grandchildPID)
}

func TestUnresponsiveTermFallsBackToKillTree(t *testing.T) {
	t.Parallel()
	closeWait := 30 * time.Millisecond
	termWait := 40 * time.Millisecond
	starter := newTreeStarter(t, "ignore-term", sessionruntime.Options{
		GracefulCloseTimeout:     closeWait,
		GracefulTerminateTimeout: termWait,
	})
	request := testRequest(t.TempDir(), "ignore-term")
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < closeWait+termWait-10*time.Millisecond || elapsed > time.Second {
		t.Fatalf("Abort duration = %s, want close+TERM grace then bounded KILL", elapsed)
	}
}

func newTreeStarter(t *testing.T, mode string, options sessionruntime.Options) *sessionruntime.Starter {
	t.Helper()
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
		domain.ProviderCodex: {
			Path: os.Args[0],
			Args: []string{"-test.run=^TestTermAwareAdapterProcess$"},
			Env:  []string{treeAdapterEnvironment + "=" + mode},
		},
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	return starter
}

func TestTermAwareAdapterProcess(t *testing.T) {
	mode := os.Getenv(treeAdapterEnvironment)
	if mode == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	if mode == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
		emit(map[string]any{
			"protocol": 1, "type": "ready", "provider_session_id": "ignore-term",
			"readiness": "protocol", "authentication": "unknown",
		})
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode != "cleanup" {
		os.Exit(51)
	}
	raw := exec.Command(os.Args[0], "-test.run=^TestSeparateRawProcess$")
	raw.Dir = "."
	raw.Env = []string{rawHelperEnvironment + "=1"}
	raw.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := raw.Start(); err != nil {
		os.Exit(52)
	}
	waitForFileInHelper("raw-grandchild.pid")
	emit(map[string]any{
		"protocol": 1, "type": "ready", "provider_session_id": "term-cleanup",
		"readiness": "protocol", "authentication": "unknown",
	})
	<-signals
	_ = syscall.Kill(-raw.Process.Pid, syscall.SIGTERM)
	_ = raw.Wait()
}

func TestSeparateRawProcess(t *testing.T) {
	if os.Getenv(rawHelperEnvironment) != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	grandchild := exec.Command(os.Args[0], "-test.run=^TestRawGrandchildProcess$")
	grandchild.Env = []string{grandchildHelperEnvironment + "=raw"}
	if err := grandchild.Start(); err != nil {
		os.Exit(53)
	}
	if err := os.WriteFile("raw.pid", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(54)
	}
	if err := os.WriteFile("raw-grandchild.pid", []byte(strconv.Itoa(grandchild.Process.Pid)), 0o600); err != nil {
		os.Exit(55)
	}
	<-signals
	_ = grandchild.Process.Signal(syscall.SIGTERM)
	_ = grandchild.Wait()
}

func TestRawGrandchildProcess(t *testing.T) {
	if os.Getenv(grandchildHelperEnvironment) != "raw" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForFileInHelper(path string) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	os.Exit(56)
}

func waitPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d survived", pid)
}

func TestForcedAbortKillsAdapterGrandchild(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "grandchild", sessionruntime.Options{
		GracefulCloseTimeout: 40 * time.Millisecond,
	})
	grandchildPID := waitGrandchildPID(t, filepath.Join(request.Workdir, "grandchild.pid"))
	t.Cleanup(func() { _ = syscall.Kill(grandchildPID, syscall.SIGKILL) })

	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(grandchildPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d survived forced Abort", grandchildPID)
}

func startGrandchildAdapter() {
	command := exec.Command(os.Args[0], "-test.run=^TestSessionRuntimeGrandchildProcess$")
	command.Env = append(os.Environ(), grandchildHelperEnvironment+"=1")
	if err := command.Start(); err != nil {
		os.Exit(41)
	}
	if err := os.WriteFile("grandchild.pid", []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		os.Exit(42)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestSessionRuntimeGrandchildProcess(t *testing.T) {
	if os.Getenv(grandchildHelperEnvironment) != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitGrandchildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(raw))
			if parseErr != nil || pid < 2 {
				t.Fatalf("invalid grandchild pid %q: %v", raw, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("grandchild pid did not appear")
	return 0
}
