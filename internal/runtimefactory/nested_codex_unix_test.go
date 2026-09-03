//go:build darwin || linux

package runtimefactory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/runtimefactory"
	"bria/internal/sessionruntime"
)

func TestNestedRuntimeFactoryAdapterCleanupKillsRawGrandchild(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	executableFixture(t, installDir, "bria-codex-adapter", true)
	rawCodex := executableFixture(t, installDir, "raw-codex", true)
	pidPath := filepath.Join(t.TempDir(), "raw-grandchild.pid")
	adapterPIDPath := filepath.Join(t.TempDir(), "adapter.pid")
	configuration := validConfig(t, installDir)
	configuration.Providers["codex"] = config.ProviderConfig{
		Enabled: true, Command: &config.ProviderCommand{Exec: rawCodex, Argv: []string{"app-server"}},
	}
	configuration.Providers["claude"] = config.ProviderConfig{Enabled: false}
	starter, err := runtimefactory.NewStarter(configuration, testEnvironment(
		"RUNTIMEFACTORY_REAL_CODEX=1",
		"RUNTIMEFACTORY_GRANDCHILD_PID_PATH="+pidPath,
		"RUNTIMEFACTORY_ADAPTER_PID_PATH="+adapterPIDPath,
	), briaExecutable, sessionruntime.Options{
		HandshakeTimeout: 5 * time.Second, GracefulCloseTimeout: 50 * time.Millisecond,
		GracefulTerminateTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewStarter() error = %v", err)
	}
	request := app.StartSessionRequest{
		SessionID: "nested-session", ComputerID: "local", Provider: domain.ProviderCodex, Workdir: t.TempDir(),
		Mode: app.SessionStartNew,
	}
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	adapterPID := waitForOnePID(t, adapterPIDPath)
	rawPID, grandchildPID := waitForNestedPIDs(t, pidPath)
	defer func() {
		_ = syscall.Kill(adapterPID, syscall.SIGCONT)
		_ = syscall.Kill(adapterPID, syscall.SIGKILL)
		_ = syscall.Kill(rawPID, syscall.SIGKILL)
		_ = syscall.Kill(grandchildPID, syscall.SIGKILL)
	}()
	for label, pid := range map[string]int{"adapter": adapterPID, "raw": rawPID, "grandchild": grandchildPID} {
		group, err := syscall.Getpgid(pid)
		if err != nil || group != adapterPID {
			t.Fatalf("%s pid %d process group = %d, error = %v, want shared adapter group %d", label, pid, group, err, adapterPID)
		}
	}
	if err := syscall.Kill(adapterPID, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP adapter: %v", err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (nestedProcessExists(rawPID) || nestedProcessExists(grandchildPID)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if nestedProcessExists(rawPID) || nestedProcessExists(grandchildPID) {
		t.Fatalf("nested raw tree survived forced adapter kill: raw=%d grandchild=%d", rawPID, grandchildPID)
	}
}

func waitForNestedPIDs(t *testing.T, path string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(encoded))
			if len(fields) == 2 {
				rawPID, rawErr := strconv.Atoi(fields[0])
				childPID, childErr := strconv.Atoi(fields[1])
				if rawErr == nil && childErr == nil && rawPID > 1 && childPID > 1 {
					return rawPID, childPID
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nested raw grandchild pid was not published")
	return 0, 0
}

func waitForOnePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(encoded)))
			if err == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("adapter pid was not published")
	return 0
}

func nestedProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
