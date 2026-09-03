package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/provider/claude"
)

const (
	treeHelperMode       = "BRIA_CLAUDE_TREE_HELPER"
	treeReadyPath        = "BRIA_CLAUDE_TREE_READY"
	treeBeatPath         = "BRIA_CLAUDE_TREE_BEAT"
	credentialMarkerPath = "BRIA_CLAUDE_CREDENTIAL_MARKER"
	testClaudeAPIKey     = "sk-ant-exact-child-only"
)

func TestMain(main *testing.M) {
	switch os.Getenv(treeHelperMode) {
	case "nested-adapter", "nested-raw", "nested-grandchild":
		runNestedProcessHelper(os.Getenv(treeHelperMode))
	case "natural-exit":
		return
	case "close-stdout":
		_ = os.Stdout.Close()
		for {
			time.Sleep(time.Hour)
		}
	case "credential-env":
		if os.Getenv("ANTHROPIC_API_KEY") == testClaudeAPIKey && os.Getenv(claude.CredentialFileEnvironment) == "" {
			_ = os.WriteFile(os.Getenv(credentialMarkerPath), []byte("ok"), 0o600)
		}
		return
	default:
		os.Exit(main.Run())
	}
}

func TestOSProcessFactoryInjectsStoredAPIKeyOnlyIntoExactClaudeChild(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	credentialPath := filepath.Join(directory, "credentials", "claude-api-key.json")
	mustWriteClaudeCredential(t, credentialPath, testClaudeAPIKey)
	marker := filepath.Join(directory, "credential-marker")
	t.Setenv(treeHelperMode, "credential-env")
	t.Setenv(credentialMarkerPath, marker)
	t.Setenv("ANTHROPIC_API_KEY", "wrong-parent-key")
	t.Setenv(claude.CredentialFileEnvironment, credentialPath)
	spec, err := claude.BuildCommandSpec(executable, nil, directory, bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	factory := &osProcessFactory{credentialPath: credentialPath}
	child, err := factory.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_, _ = io.Copy(io.Discard, child.Stdout())
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		_ = child.Kill()
		t.Fatal("credential helper did not exit")
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "ok" {
		t.Fatalf("exact child credential marker = %q, %v", body, err)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "wrong-parent-key" {
		t.Fatal("factory mutated parent-wide ANTHROPIC_API_KEY")
	}
}

func mustWriteClaudeCredential(t *testing.T, path, apiKey string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct {
		Version     int    `json:"version"`
		OperationID string `json:"operation_id"`
		ComputerID  string `json:"computer_id"`
		APIKey      []byte `json:"api_key"`
	}{Version: 1, OperationID: "test-operation", ComputerID: "local", APIKey: []byte(apiKey)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOSProcessEOFStillSignalsChildBeforeWait(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	t.Setenv(treeHelperMode, "close-stdout")
	spec, err := claude.BuildCommandSpec(executable, nil, t.TempDir(), bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatalf("BuildCommandSpec() error = %v", err)
	}
	credentialPath := filepath.Join(t.TempDir(), "credentials", "claude-api-key.json")
	mustWriteClaudeCredential(t, credentialPath, testClaudeAPIKey)
	factory := &osProcessFactory{credentialPath: credentialPath}
	child, err := factory.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := io.Copy(io.Discard, child.Stdout()); err != nil {
		t.Fatalf("read closed stdout: %v", err)
	}
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		_ = child.Kill()
		t.Fatal("EOF reaper waited forever for a child that kept running")
	}
	if err := child.Kill(); err != nil {
		t.Fatalf("Kill(after EOF reap) error = %v", err)
	}
}

func TestOSProcessAbortRacesNaturalExitWithoutPostReapSignal(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	directory := t.TempDir()
	credentialPath := filepath.Join(directory, "credentials", "claude-api-key.json")
	mustWriteClaudeCredential(t, credentialPath, testClaudeAPIKey)
	t.Setenv(treeHelperMode, "natural-exit")
	for iteration := 0; iteration < 100; iteration++ {
		spec, err := claude.BuildCommandSpec(executable, nil, directory, bytes.NewReader(make([]byte, 16)))
		if err != nil {
			t.Fatalf("BuildCommandSpec(%d) error = %v", iteration, err)
		}
		factory := &osProcessFactory{credentialPath: credentialPath}
		child, err := factory.Start(context.Background(), spec)
		if err != nil {
			t.Fatalf("Start(%d) error = %v", iteration, err)
		}
		readDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, child.Stdout())
			close(readDone)
		}()
		killDone := make(chan error, 1)
		go func() { killDone <- child.Kill() }()
		select {
		case <-child.Done():
		case <-time.After(time.Second):
			t.Fatalf("Done(%d) did not close", iteration)
		}
		if err := <-killDone; err != nil {
			t.Fatalf("Kill(%d) error = %v", iteration, err)
		}
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Fatalf("stdout reader(%d) did not finish", iteration)
		}
		if err := child.Kill(); err != nil {
			t.Fatalf("Kill(%d after reap) error = %v", iteration, err)
		}
	}
}

func TestRunRejectsMissingOrRelativeExecutableWithoutStartingProcess(t *testing.T) {
	for _, args := range [][]string{nil, {"claude"}, {"/usr/local/bin/claude"}, {"--", "claude"}} {
		var stdout, stderr bytes.Buffer
		code := run(
			context.Background(),
			args,
			io.NopCloser(strings.NewReader("")),
			&stdout,
			&stderr,
			bytes.NewReader(make([]byte, 16)),
			failingFactory{},
		)
		if code != 2 {
			t.Fatalf("run(%#v) code = %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("run(%#v) stdout = %q", args, stdout.String())
		}
	}
}

func TestRunSelectsExactResumeFromRuntimeContractWithoutGeneratingID(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	t.Setenv("BRIA_START_MODE", "resume")
	t.Setenv("BRIA_PROVIDER_SESSION_ID", "00000000-0000-4000-8000-000000000000")
	factory := &recordingFailingFactory{}
	var stdout, stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"--", executable},
		io.NopCloser(strings.NewReader("")),
		&stdout,
		&stderr,
		panicReader{},
		factory,
	)
	if code != 1 {
		t.Fatalf("run(resume) code = %d, want 1 after recorded child-start failure", code)
	}
	wantSuffix := []string{"--resume", "00000000-0000-4000-8000-000000000000"}
	if len(factory.spec.Args) < len(wantSuffix) ||
		!equalStrings(factory.spec.Args[len(factory.spec.Args)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("resume args = %#v, want suffix %#v", factory.spec.Args, wantSuffix)
	}
	if factory.spec.SessionID != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("resume session = %q", factory.spec.SessionID)
	}
}

func TestRunRejectsAmbiguousOrIncompleteRuntimeStartMode(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	for _, test := range []struct {
		name      string
		mode      string
		sessionID string
	}{
		{name: "missing mode"},
		{name: "resume without id", mode: "resume"},
		{name: "new with existing id", mode: "new", sessionID: "00000000-0000-4000-8000-000000000000"},
		{name: "unknown mode", mode: "replace", sessionID: "00000000-0000-4000-8000-000000000000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BRIA_START_MODE", test.mode)
			t.Setenv("BRIA_PROVIDER_SESSION_ID", test.sessionID)
			factory := &recordingFailingFactory{}
			var stdout, stderr bytes.Buffer
			code := run(
				context.Background(), []string{"--", executable},
				io.NopCloser(strings.NewReader("")), &stdout, &stderr,
				bytes.NewReader(make([]byte, 16)), factory,
			)
			if code != 2 {
				t.Fatalf("run() code = %d, want 2", code)
			}
			if factory.started {
				t.Fatal("invalid start mode reached process factory")
			}
		})
	}
}

func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			return string(content)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper file: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper file %q did not appear", filepath.Base(path))
	return ""
}

func waitForChangingFile(t *testing.T, path, previous string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current := waitForFile(t, path, timeout)
		if current != previous {
			return current
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper file %q did not change", filepath.Base(path))
	return ""
}

type failingFactory struct{}

func (failingFactory) Start(context.Context, claude.CommandSpec) (claude.ChildProcess, error) {
	return nil, claude.ErrChildStart
}

type recordingFailingFactory struct {
	spec    claude.CommandSpec
	started bool
}

func (factory *recordingFailingFactory) Start(_ context.Context, spec claude.CommandSpec) (claude.ChildProcess, error) {
	factory.spec = spec
	factory.started = true
	return nil, claude.ErrChildStart
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("resume must not read randomness") }

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
