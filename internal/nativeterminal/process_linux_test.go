//go:build linux

package nativeterminal

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"bria/internal/terminalbinding"
)

func TestCloseKillsHUPIgnoringChildAndDoesNotInheritHostEnvironment(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Setenv("BRIA_TERMINAL_HOST_SECRET", "must-not-inherit")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	term, err := Open(ctx, Config{Command: []string{"/bin/sh", "-c", `trap '' HUP; /bin/sh -c 'trap "" HUP; while :; do /bin/sleep 1; done' & printf 'CHILD:%s HOST:%s\n' "$!" "$BRIA_TERMINAL_HOST_SECRET"; wait`}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close(context.Background())
	waitScreen(t, ctx, term, "CHILD:")
	screen, err := term.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen, "must-not-inherit") {
		t.Fatal("inherited ambient environment")
	}
	pidText := strings.Fields(strings.SplitN(screen, "CHILD:", 2)[1])[0]
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	child, err := terminalbinding.OwnProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer child.KillTree()
	if err := term.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !child.Exited() {
		t.Fatal("HUP ignoring child survived close")
	}
	if _, err := os.Stat(term.dir); !os.IsNotExist(err) {
		t.Fatalf("socket directory remained: %v", err)
	}
}
