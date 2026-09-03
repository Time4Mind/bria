//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/provider/claude"
	"bria/internal/sessionruntime"
)

const nestedWorkdir = "BRIA_CLAUDE_NESTED_WORKDIR"

func TestSessionRuntimeForcedKillReachesStoppedClaudeAdapterRawAndGrandchild(t *testing.T) {
	directory := t.TempDir()
	ready := filepath.Join(directory, "nested-ready")
	beat := filepath.Join(directory, "nested-beat")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	environment := replaceEnvironment(os.Environ(), treeHelperMode, "nested-adapter")
	environment = replaceEnvironment(environment, treeReadyPath, ready)
	environment = replaceEnvironment(environment, treeBeatPath, beat)
	environment = replaceEnvironment(environment, nestedWorkdir, directory)
	credentialPath := filepath.Join(directory, "credentials", "claude-api-key.json")
	mustWriteClaudeCredential(t, credentialPath, testClaudeAPIKey)
	starter, err := sessionruntime.NewStarter(
		map[domain.Provider]sessionruntime.CommandSpec{
			domain.ProviderClaude: {Path: executable, Env: environment, ProviderCredentialFile: credentialPath},
		},
		sessionruntime.Options{
			HandshakeTimeout:         2 * time.Second,
			GracefulCloseTimeout:     30 * time.Millisecond,
			GracefulTerminateTimeout: 30 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatalf("NewStarter() error = %v", err)
	}
	request := app.StartSessionRequest{
		SessionID: "nested-logical", ComputerID: "local", Provider: domain.ProviderClaude, Workdir: directory,
		Mode: app.SessionStartNew,
	}
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	firstBeat := waitForChangingFile(t, beat, "", time.Second)
	_ = waitForChangingFile(t, beat, firstBeat, time.Second)
	adapterPID, err := strconv.Atoi(waitForFile(t, ready, time.Second))
	if err != nil || adapterPID < 2 {
		t.Fatalf("decode stopped adapter pid: %v", err)
	}
	abortDone := make(chan error, 1)
	go func() { abortDone <- starter.Abort(context.Background(), request, binding) }()
	select {
	case err := <-abortDone:
		if err != nil {
			t.Fatalf("Abort() error = %v", err)
		}
	case <-time.After(time.Second):
		_ = syscall.Kill(-adapterPID, syscall.SIGKILL)
		<-abortDone
		t.Fatal("Abort did not reach forced process-tree kill")
	}
	stable := waitForFile(t, beat, time.Second)
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(beat)
	if err != nil {
		t.Fatalf("read nested heartbeat after kill: %v", err)
	}
	if string(after) != stable {
		t.Fatalf("raw Claude grandchild survived forced adapter kill: before=%q after=%q", stable, after)
	}
}

func runNestedProcessHelper(mode string) {
	switch mode {
	case "nested-adapter":
		if err := os.Setenv(treeHelperMode, "nested-raw"); err != nil {
			os.Exit(91)
		}
		executable, err := os.Executable()
		if err != nil {
			os.Exit(92)
		}
		spec, err := claude.BuildCommandSpec(
			executable, nil, os.Getenv(nestedWorkdir), bytes.NewReader(make([]byte, 16)),
		)
		if err != nil {
			os.Exit(93)
		}
		factory := &osProcessFactory{credentialPath: os.Getenv(claude.CredentialFileEnvironment)}
		if _, err := factory.Start(context.Background(), spec); err != nil {
			os.Exit(94)
		}
		if waitForHelperPath(os.Getenv(treeReadyPath), 2*time.Second) != nil {
			os.Exit(95)
		}
		if err := os.WriteFile(os.Getenv(treeReadyPath), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(96)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
			"protocol": 1, "type": "ready",
			"provider_session_id": "00000000-0000-4000-8000-000000000000",
			"readiness":           "protocol", "authentication": "unknown",
		}); err != nil {
			os.Exit(97)
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
			os.Exit(98)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "nested-raw":
		signal.Ignore(syscall.SIGTERM)
		executable, err := os.Executable()
		if err != nil {
			os.Exit(99)
		}
		grandchild := exec.Command(executable)
		grandchild.Env = replaceEnvironment(os.Environ(), treeHelperMode, "nested-grandchild")
		if err := grandchild.Start(); err != nil {
			os.Exit(100)
		}
		if waitForHelperPath(os.Getenv(treeBeatPath), 2*time.Second) != nil {
			os.Exit(101)
		}
		if err := os.WriteFile(os.Getenv(treeReadyPath), []byte("ready"), 0o600); err != nil {
			os.Exit(102)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "nested-grandchild":
		signal.Ignore(syscall.SIGTERM)
		for counter := 1; ; counter++ {
			if err := os.WriteFile(os.Getenv(treeBeatPath), []byte(time.Now().String()), 0o600); err != nil {
				os.Exit(103)
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(104)
	}
}

func waitForHelperPath(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("helper path timeout")
}
