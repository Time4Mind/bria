package nativeterminal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOwnedTerminalExpansionPreservesWidthAndDoesNotShrink(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native tmux platform")
	}
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip(err)
	}
	root, err := os.MkdirTemp("/tmp", "bria-expand-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	term, err := Open(ctx, Config{Command: []string{cat}, SocketDir: root, Workdir: filepath.Dir(root), Environment: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close(context.Background())
	if err := term.Expand(ctx, 80); err != nil {
		t.Fatal(err)
	}
	if err := term.Expand(ctx, 40); err != nil {
		t.Fatal(err)
	}
	size, err := term.run(ctx, nil, "display-message", "-p", "-t", "cli:0.0", "#{window_width}x#{window_height}")
	if err != nil || strings.TrimSpace(size) != "120x80" {
		t.Fatal("canvas changed incorrectly", size, err)
	}
	if err := term.Expand(ctx, 300); err == nil {
		t.Fatal("unbounded expansion allowed")
	}
}
