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
)

func TestPersistentStartupFailureAfterBindingClosesOwnedTerminal(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, stateDir string)
		output  io.Writer
		stage   StartupStage
	}{
		{
			name: "receipt-baseline",
			prepare: func(t *testing.T, stateDir string) {
				t.Helper()
				if err := os.MkdirAll(stateDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stateDir, fixtureSession+".json"), []byte("malformed"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			output: &bytes.Buffer{},
			stage:  StartupStageReceiptBaseline,
		},
		{
			name:    "protocol-ready-emission",
			prepare: func(*testing.T, string) {},
			output:  failingWriter{err: errors.New("private output failure")},
			stage:   StartupStageProtocolReadyEmission,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "state")
			t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
			t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
			test.prepare(t, stateDir)
			binding := nativeterminal.Binding{LogicalSessionID: "logical-failed-start", NativeSessionID: fixtureSession, Provider: "codex", Workdir: root}
			t.Cleanup(func() {
				if terminal, err := nativeterminal.AttachExisting(context.Background(), stateDir, binding); err == nil {
					_ = terminal.Close(context.Background())
				}
			})
			err := Run(context.Background(), io.NopCloser(strings.NewReader("")), test.output, Config{
				Persistent: true, LogicalSessionID: binding.LogicalSessionID, Provider: domain.ProviderCodex,
				Command: []string{os.Args[0]}, Workdir: root, ResumeID: fixtureSession, StateDir: stateDir, Environment: os.Environ(),
			})
			if err == nil {
				t.Fatal("startup failure was not propagated")
			}
			if got := StartupFailureStage(err); got != test.stage {
				t.Fatalf("startup stage=%q want %q", got, test.stage)
			}
			terminal, attachErr := nativeterminal.AttachExisting(context.Background(), stateDir, binding)
			if terminal != nil {
				_ = terminal.Close(context.Background())
			}
			if !errors.Is(attachErr, nativeterminal.ErrNotManaged) {
				t.Fatalf("failed startup left an attachable terminal or binding: %v", attachErr)
			}
			paths, globErr := filepath.Glob(filepath.Join(stateDir, "terminal-bindings", "*.json"))
			if globErr != nil || len(paths) != 0 {
				t.Fatalf("failed startup left binding records: %v %v", paths, globErr)
			}
		})
	}
}

func TestAttachedPersistentStartupFailureDetachesAndPreservesExistingTerminal(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
	config := Config{
		Persistent: true, LogicalSessionID: "logical-existing", Provider: domain.ProviderCodex,
		Command: []string{os.Args[0]}, Workdir: root, ResumeID: fixtureSession, StateDir: stateDir, Environment: os.Environ(),
	}
	binding := nativeterminal.Binding{
		LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "codex", Workdir: root,
	}
	first := startPersistentAdapterFixture(t, config)
	if ready := first.receive(t); ready.Type != "ready" {
		t.Fatalf("initial Ready: %+v", ready)
	}
	stopPersistentAdapterEOF(t, first)
	if err := os.WriteFile(filepath.Join(stateDir, fixtureSession+".json"), []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), io.NopCloser(strings.NewReader("")), io.Discard, Config{
		Persistent: true, AttachOnly: true, LogicalSessionID: binding.LogicalSessionID, Provider: domain.ProviderCodex,
		Workdir: root, ResumeID: fixtureSession, StateDir: stateDir, Environment: os.Environ(),
	})
	if err == nil || StartupFailureStage(err) != StartupStageReceiptBaseline {
		t.Fatalf("post-attach pre-Ready failure missing: stage=%q err=%v", StartupFailureStage(err), err)
	}
	terminal, attachErr := nativeterminal.AttachExisting(context.Background(), stateDir, binding)
	if attachErr != nil {
		t.Fatalf("post-attach failure destroyed existing terminal or binding: %v", attachErr)
	}
	if err := terminal.Close(context.Background()); err != nil {
		t.Fatalf("existing terminal cleanup: %v", err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStartupStagesAtRunBoundary(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Run("open-terminal", func(t *testing.T) {
		err := Run(context.Background(), io.NopCloser(strings.NewReader("")), io.Discard, Config{Persistent: true})
		if got := StartupFailureStage(err); got != StartupStageOpenTerminal {
			t.Fatalf("startup stage=%q want %q: %v", got, StartupStageOpenTerminal, err)
		}
	})
	t.Run("native-readiness-status", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
		t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
		t.Setenv("NATIVE_ADAPTER_FIXTURE_NOT_READY", "1")
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		err := Run(ctx, io.NopCloser(strings.NewReader("")), io.Discard, Config{
			Provider: domain.ProviderCodex, Command: []string{os.Args[0]}, Workdir: root, Environment: os.Environ(),
		})
		if got := StartupFailureStage(err); got != StartupStageNativeReadinessStatus {
			t.Fatalf("startup stage=%q want %q: %v", got, StartupStageNativeReadinessStatus, err)
		}
	})
	t.Run("binding-persistence", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "not-a-directory")
		if err := os.WriteFile(stateDir, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
		t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
		err := Run(context.Background(), io.NopCloser(strings.NewReader("")), io.Discard, Config{
			Persistent: true, LogicalSessionID: "logical-binding-failure", Provider: domain.ProviderCodex,
			Command: []string{os.Args[0]}, Workdir: root, StateDir: stateDir, Environment: os.Environ(),
		})
		if got := StartupFailureStage(err); got != StartupStageBindingPersistence {
			t.Fatalf("startup stage=%q want %q: %v", got, StartupStageBindingPersistence, err)
		}
	})
}
