//go:build linux || darwin

package nativeterminal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/processgroup"
)

func TestPersistentFakeAdapterOwner(t *testing.T) {
	root := os.Getenv("BRIA_PERSISTENT_FAKE_OWNER")
	if root == "" {
		return
	}
	ctx := context.Background()
	term, err := Open(ctx, Config{Persistent: true, SocketDir: root, Workdir: root, Environment: os.Environ(),
		Command: []string{"/bin/sh", "-c", `printf 'CLI-PID:%s\n' "$$"; while IFS= read -r line; do printf 'ACCEPTED:%s\n' "$line"; done`}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close(ctx)
	if err := term.PersistBinding(ctx, root, Binding{LogicalSessionID: "logical-child", NativeSessionID: "native-child", Provider: "fake", Workdir: root}); err != nil {
		t.Fatal(err)
	}
	if err := term.Input(ctx, "exact-child-request"); err != nil {
		t.Fatal(err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	waitScreen(t, readyCtx, term, "ACCEPTED:exact-child-request")
	screen, err := term.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ready"), []byte(screen), 0600); err != nil {
		t.Fatal(err)
	}
	// The parent kills only this disposable adapter's verified process group.
	for {
		time.Sleep(time.Hour)
	}
}

func TestPersistentCLISurvivesAdapterProcessGroupKill(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root, err := os.MkdirTemp("/tmp", "bria-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestPersistentFakeAdapterOwner$", "-test.count=1")
	cmd.Env = append(os.Environ(), "BRIA_PERSISTENT_FAKE_OWNER="+root)
	if err := processgroup.Configure(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = processgroup.KillTree(cmd)
			_ = cmd.Wait()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var before []byte
	for {
		before, err = os.ReadFile(filepath.Join(root, "ready"))
		if err == nil && strings.Contains(string(before), "ACCEPTED:exact-child-request") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("fake adapter did not confirm accepted input")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := processgroup.KillTree(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("fake adapter unexpectedly exited normally")
	}
	attached, err := AttachExisting(ctx, root, Binding{LogicalSessionID: "logical-child", NativeSessionID: "native-child", Provider: "fake", Workdir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer attached.Close(context.Background())
	after, err := attached.Capture(ctx)
	if err != nil || after != string(before) || strings.Count(after, "ACCEPTED:exact-child-request") != 1 {
		t.Fatalf("adapter death lost exact CLI/accepted input: %q %v", after, err)
	}
	if err := attached.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
