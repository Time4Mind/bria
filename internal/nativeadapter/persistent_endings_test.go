package nativeadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/nativeterminal"
	"bria/internal/runtimeprotocol"
)

func TestPersistentAdapterDetachCancellationAndProtocolErrorPreserveCLI(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	for _, ending := range []string{"detach", "cancel", "protocol-error"} {
		t.Run(ending, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
			t.Setenv("NATIVE_ADAPTER_FIXTURE", "1")
			config := Config{Persistent: true, LogicalSessionID: "logical-ending", Provider: domain.ProviderCodex,
				Command: []string{os.Args[0]}, Workdir: root, ResumeID: fixtureSession, StateDir: filepath.Join(root, "state"), Environment: os.Environ()}
			t.Cleanup(func() {
				if term, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, nativeterminal.Binding{
					LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "codex", Workdir: root}); err == nil {
					_ = term.Close(context.Background())
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := startPersistentAdapterFixture(t, config, ctx)
			if ready := h.receive(t); ready.Type != runtimeprotocol.TypeReady {
				t.Fatalf("Ready: %+v", ready)
			}
			if ending == "detach" {
				h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeDetach})
			} else if ending == "cancel" {
				cancel()
			} else if _, err := h.input.Write([]byte("not-json\n")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-h.done:
				h.done <- err
				if (err == nil) != (ending == "detach") {
					t.Fatalf("wrong observer termination result: %v", err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("observer did not exit")
			}
			term, err := nativeterminal.AttachExisting(context.Background(), config.StateDir, nativeterminal.Binding{
				LogicalSessionID: config.LogicalSessionID, NativeSessionID: fixtureSession, Provider: "codex", Workdir: root})
			if err != nil {
				t.Fatalf("observer failure destroyed CLI: %v", err)
			}
			if err := term.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
