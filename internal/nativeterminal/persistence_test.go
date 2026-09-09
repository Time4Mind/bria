//go:build linux || darwin

package nativeterminal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPersistentDetachAttachesSameCLIWithoutInputReplay(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root, err := os.MkdirTemp("/tmp", "bria-persist-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	binding := Binding{LogicalSessionID: "logical-1", NativeSessionID: "native-1", Provider: "fake", Workdir: root}
	term, err := Open(ctx, Config{Persistent: true, SocketDir: root, Workdir: root, Environment: os.Environ(),
		Command: []string{"/bin/sh", "-c", `printf 'READY:%s\n' "$$"; while IFS= read -r line; do printf 'ACCEPTED:%s\n' "$line"; done`}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close(context.Background())
	if err := term.PersistBinding(ctx, root, binding); err != nil {
		t.Fatal(err)
	}
	if err := term.Input(ctx, "one-request"); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, term, "ACCEPTED:one-request")
	before, err := term.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AttachExisting(ctx, root, binding); !errors.Is(err, ErrAlreadyAttached) {
		t.Fatalf("concurrent observer accepted: %v", err)
	}
	if err := term.Detach(ctx); err != nil {
		t.Fatal(err)
	}
	if err := term.Input(ctx, "must-not-dispatch"); err == nil {
		t.Fatal("detached handle accepted input")
	}
	attached, err := AttachExisting(ctx, root, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer attached.Close(context.Background())
	after, err := attached.Capture(ctx)
	if err != nil || after != before || strings.Count(after, "ACCEPTED:one-request") != 1 {
		t.Fatalf("attach changed accepted CLI screen: %q %v", after, err)
	}
	if alive, err := attached.Alive(ctx); !alive || err != nil {
		t.Fatalf("attached CLI not alive: %t %v", alive, err)
	}
	if err := attached.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := AttachExisting(ctx, root, binding); !errors.Is(err, ErrTerminalUnavailable) {
		t.Fatalf("closed CLI masquerades as live: %v", err)
	}
}
