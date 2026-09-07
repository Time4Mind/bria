package nativeterminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInputSettlesAfterPasteBeforeEnter(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("NATIVE_PASTE_TEST_LOG", log)
	term := &Terminal{path: self, socket: "unused", literalInputBarrier: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := term.Input(ctx, "/status"); err != nil {
		t.Fatal(err)
	}
	pasted, err := os.ReadFile(log + ".input")
	if err != nil || string(pasted) != "/status " {
		t.Fatalf("Codex literal barrier=%q error=%v", pasted, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("calls=%q", data)
	}
	paste := strings.Fields(lines[1])
	enter := strings.Fields(lines[2])
	if paste[0] != "paste-buffer" || enter[0] != "send-keys" {
		t.Fatalf("calls=%q", data)
	}
	pasteAt, _ := strconv.ParseInt(paste[1], 10, 64)
	enterAt, _ := strconv.ParseInt(enter[1], 10, 64)
	if elapsed := time.Duration(enterAt - pasteAt); elapsed < pasteSettle {
		t.Fatalf("Enter followed paste after %s; need %s", elapsed, pasteSettle)
	}
}

func TestCanceledPasteDoesNotDispatchEnter(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("NATIVE_PASTE_TEST_LOG", log)
	term := &Terminal{path: self, socket: "unused"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- term.Input(ctx, "/status") }()
	for {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), "paste-buffer") {
			cancel()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("paste not reached")
		case <-time.After(time.Millisecond):
		}
	}
	if err := <-done; err == nil {
		t.Fatal("canceled input succeeded")
	}
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "send-keys") {
		t.Fatalf("Enter dispatched after cancellation: %q", data)
	}
}

func TestMain(m *testing.M) {
	if path := os.Getenv("NATIVE_PASTE_TEST_LOG"); path != "" && len(os.Args) > 4 && os.Args[1] == "-u" {
		if os.Args[4] == "paste-buffer" {
			for _, arg := range os.Args[5:] {
				if arg == "-p" {
					os.Exit(4)
				}
			}
		}
		if os.Args[4] == "load-buffer" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil || os.WriteFile(path+".input", data, 0600) != nil {
				os.Exit(2)
			}
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		_, err = fmt.Fprintf(file, "%s %d\n", os.Args[4], time.Now().UnixNano())
		_ = file.Close()
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if os.Getenv("BRIA_FAKE_TMUX_STARTUP") == "1" && len(os.Args) > 1 && os.Args[1] == "-u" {
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func TestFailedServerIsReapedAndPrivateDirectoryRemoved(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux transport")
	}
	bin := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env unavailable")
	}
	if err := os.Symlink(self, filepath.Join(bin, "tmux")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(env, filepath.Join(bin, "env")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("BRIA_FAKE_TMUX_STARTUP", "1")
	parent := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Open(ctx, Config{SocketDir: parent, Command: []string{"unused"}}); err == nil {
		t.Fatal("failed server accepted")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed server left socket state: %v %v", entries, err)
	}
}

func TestOwnedTerminalLiteralInputAndCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux transport")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	parent := t.TempDir()
	term, err := Open(ctx, Config{SocketDir: parent, Workdir: parent, Environment: []string{"BRIA_TERMINAL_TEST=visible"}, Command: []string{"/bin/sh", "-c", `printf 'READY:%s\n' "$BRIA_TERMINAL_TEST"; while IFS= read -r line; do printf 'INPUT:%s\n' "$line"; done`}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := term.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	info, err := os.Stat(term.dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private socket directory: %v %v", info, err)
	}
	waitScreen(t, ctx, term, "READY:visible")
	if alive, err := term.Alive(ctx); !alive || err != nil {
		t.Fatalf("alive=%v error=%v", alive, err)
	}
	input := `$(touch SHOULD_NOT_EXIST); /model --literal 'quoted'`
	if err := term.Input(ctx, input); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, term, "INPUT:"+input)
	if _, err := os.Stat(filepath.Join(parent, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
		t.Fatalf("input executed as shell: %v", err)
	}
	if err := term.Key(ctx, "kill-server"); err == nil {
		t.Fatal("accepted arbitrary command as key")
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := term.Close(canceled); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.done:
	default:
		t.Fatal("server not reaped")
	}
	if _, err := os.Stat(term.dir); !os.IsNotExist(err) {
		t.Fatalf("socket directory remains: %v", err)
	}
	if alive, err := term.Alive(ctx); alive || err != nil {
		t.Fatalf("closed alive=%v error=%v", alive, err)
	}
	if err := term.Input(ctx, "x"); err == nil {
		t.Fatal("closed terminal accepted input")
	}
}

func TestOpenFailureAndCancellationLeaveNoSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux transport")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	parent := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	term, err := Open(ctx, Config{SocketDir: parent, Workdir: filepath.Join(parent, "missing"), Command: []string{"/bin/cat"}})
	if term != nil {
		defer term.Close(context.Background())
	}
	if err == nil {
		t.Fatal("invalid working directory accepted")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatalf("failed open leaked %d directories", len(entries))
	}
	cancel()
	if _, err := Open(ctx, Config{SocketDir: parent, Command: []string{"/bin/cat"}}); err == nil {
		t.Fatal("canceled open succeeded")
	}
}

func waitScreen(t *testing.T, ctx context.Context, term *Terminal, want string) {
	t.Helper()
	for {
		got, err := term.Capture(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, want) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("screen missing %q: %q", want, got)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
