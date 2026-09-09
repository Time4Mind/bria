package nativeadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/nativeterminal"
	"bria/internal/runtimeprotocol"
)

func TestPersistentExplicitResumeAttachesManagedCLIWithoutStartupInput(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
	config := Config{Persistent: true, LogicalSessionID: "logical-resume", Provider: domain.ProviderCodex,
		Command: []string{os.Args[0]}, Workdir: root, ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ()}
	// An explicit historical resume may create a terminal when none is managed.
	first := startPersistentAdapterFixture(t, config)
	if ready := first.receive(t); ready.Type != runtimeprotocol.TypeReady || ready.ProviderSessionID != fixtureSession {
		t.Fatalf("explicit missing resume: %+v", ready)
	}
	stopPersistentAdapterEOF(t, first)
	binding := nativeterminal.Binding{LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "codex", Workdir: root}
	t.Cleanup(func() {
		if terminal, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding); err == nil {
			_ = terminal.Close(context.Background())
		}
	})
	terminal, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, binding)
	if err != nil {
		t.Fatal(err)
	}
	before, err := terminal.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := terminal.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(config.StateDir, "terminal-bindings", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("binding count: %v %v", paths, err)
	}
	manifest, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	// Neither launch nor transcript baseline is permitted for a managed resume.
	transcript := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "rollout-"+fixtureSession+".jsonl")
	file, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(file, `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":"`+strings.Repeat("x", 2<<20)+`"}}`+"\n")
	if err := errors.Join(err, file.Close()); err != nil {
		t.Fatal(err)
	}
	config.Command = []string{"/does/not/exist/must-not-launch"}
	wrongIdentity := config
	wrongIdentity.LogicalSessionID = "other-logical-session"
	assertPersistentResumeFailure(t, wrongIdentity, nativeterminal.ErrBindingMismatch)
	if err := os.WriteFile(paths[0], []byte("malformed-existing-binding"), 0600); err != nil {
		t.Fatal(err)
	}
	assertPersistentResumeFailure(t, config, nativeterminal.ErrBindingMismatch)
	if err := os.WriteFile(paths[0], manifest, 0600); err != nil {
		t.Fatal(err)
	}
	second := startPersistentAdapterFixture(t, config)
	if ready := second.receive(t); ready.Type != runtimeprotocol.TypeReady || ready.ProviderSessionID != fixtureSession {
		t.Fatalf("managed resume: %+v", ready)
	}
	second.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "screen"})
	if screen := second.receive(t); screen.Type != runtimeprotocol.TypeNativeSnapshot || screen.FullText != before {
		t.Fatalf("resume changed CLI screen: %+v", screen)
	}
	after, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(manifest, after) {
		t.Fatalf("resume replaced exact PID/birth/socket binding: %v", err)
	}
	// Even explicit resume must not launch over an existing observer lease.
	assertPersistentResumeFailure(t, config, nativeterminal.ErrAlreadyAttached)
	second.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeClose})
	if closed := second.receive(t); closed.Type != runtimeprotocol.TypeClosed {
		t.Fatalf("missing close ack: %+v", closed)
	}
	stopPersistentAdapterEOF(t, second)
}

func TestStrictAttachMissingDoesNotFallBackToResume(t *testing.T) {
	root := t.TempDir()
	config := Config{Persistent: true, AttachOnly: true, LogicalSessionID: "logical", Provider: domain.ProviderCodex,
		Command: []string{"/must/not/launch"}, ResumeID: fixtureSession, Workdir: root, StateDir: filepath.Join(root, "state")}
	assertPersistentResumeFailure(t, config, nativeterminal.ErrNotManaged)
	if _, err := os.Lstat(config.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict attach created state: %v", err)
	}
}

func assertPersistentResumeFailure(t *testing.T, config Config, want error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var output bytes.Buffer
	err := Run(ctx, io.NopCloser(strings.NewReader("")), &output, config)
	if !errors.Is(err, want) || output.Len() != 0 {
		t.Fatalf("unsafe resume fallback: err=%v want=%v output bytes=%d", err, want, output.Len())
	}
}
