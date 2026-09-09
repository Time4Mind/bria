//go:build darwin

package nativeterminal

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"bria/internal/processgroup"
	"bria/internal/terminalbinding"
)

// This opt-in probe creates only a random disposable launchd job. It does not
// adopt a terminal, read user state, or authorize any production handoff.
type handoffIdentity struct {
	Owner, Adapter, Server, Pane                     int
	OwnerBirth, AdapterBirth, ServerBirth, PaneBirth string
	Socket                                           string
	Device, Inode                                    uint64
	Screen                                           string
}

func TestFirstHandoffFixture(t *testing.T) {
	root := os.Getenv("BRIA_HANDOFF_FIXTURE_ROOT")
	role := os.Getenv("BRIA_HANDOFF_FIXTURE_ROLE")
	if root == "" || role == "" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if role == "owner" {
		stopping := make(chan os.Signal, 1)
		signal.Notify(stopping, syscall.SIGTERM)
		defer signal.Stop(stopping)
		cmd := exec.Command(self, "-test.run=^TestFirstHandoffFixture$", "-test.count=1")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BRIA_HANDOFF_FIXTURE_ROOT=" + root, "BRIA_HANDOFF_FIXTURE_ROLE=adapter"}
		if err := processgroup.Configure(cmd); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = processgroup.KillTree(cmd); _ = cmd.Wait() }()
		<-stopping
		// Model the old owner's destructive shutdown, not the new detach path.
		return
	}
	if role != "adapter" {
		t.Fatal("unknown disposable fixture role")
	}
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM)
	defer signal.Stop(stopping)
	term, err := Open(ctx, Config{SocketDir: root, Workdir: root, Environment: os.Environ(),
		Command: []string{"/bin/sh", "-c", `while IFS= read -r line; do printf 'ACCEPTED:%s\n' "$line"; done`}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close(ctx)
	if err := term.Input(ctx, "disposable-exact-input"); err != nil {
		t.Fatal(err)
	}
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	waitScreen(t, ready, term, "ACCEPTED:disposable-exact-input")
	paneText, err := term.run(ready, nil, "display-message", "-p", "-t", "cli:0.0", "#{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	pane, err := strconv.Atoi(strings.TrimSpace(paneText))
	if err != nil {
		t.Fatal(err)
	}
	r := handoffIdentity{Owner: os.Getppid(), Adapter: os.Getpid(), Server: term.server.Process.Pid, Pane: pane, Socket: term.socket}
	for _, pair := range []struct {
		pid   int
		birth *string
	}{{r.Owner, &r.OwnerBirth}, {r.Adapter, &r.AdapterBirth}, {r.Server, &r.ServerBirth}, {r.Pane, &r.PaneBirth}} {
		*pair.birth, err = terminalbinding.ProcessBirth(ready, pair.pid)
		if err != nil {
			t.Fatal(err)
		}
	}
	r.Device, r.Inode, err = terminalbinding.SocketIdentity(r.Socket)
	if err != nil {
		t.Fatal(err)
	}
	r.Screen, err = term.Capture(ready)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ready.tmp"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "ready.tmp"), filepath.Join(root, "ready.json")); err != nil {
		t.Fatal(err)
	}
	<-stopping
}

func TestDisposableLaunchdFirstHandoff(t *testing.T) {
	if os.Getenv("BRIA_DISPOSABLE_LAUNCHD_PROOF") != "1" {
		t.Skip("opt-in disposable launchd mutation proof")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal(err)
	}
	t.Run("fenced", func(t *testing.T) { runDisposableHandoff(t, true) })
	t.Run("unsafe-shutdown-negative-control", func(t *testing.T) { runDisposableHandoff(t, false) })
}

func runDisposableHandoff(t *testing.T, fenced bool) {
	root, err := os.MkdirTemp("/private/tmp", "bria-handoff-")
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			t.Logf("retained disposable cleanup evidence: %s", root)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	label := "com.time4mind.bria.handoff-proof." + filepath.Base(root)
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	plist := filepath.Join(root, "job.plist")
	content := fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict>
<key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>-test.run=^TestFirstHandoffFixture$</string><string>-test.count=1</string></array>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string><key>BRIA_HANDOFF_FIXTURE_ROOT</key><string>%s</string><key>BRIA_HANDOFF_FIXTURE_ROLE</key><string>owner</string></dict>
<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ExitTimeOut</key><integer>1</integer><key>ProcessType</key><string>Background</string>
<key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string></dict></plist>`, escape(label), escape(self), escape(os.Getenv("PATH")), escape(root))
	if err := os.WriteFile(plist, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var r handoffIdentity
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		// Resume only our still-identical disposable owners before normal cleanup.
		_ = handoffSignal(cleanup, r.Owner, r.OwnerBirth, syscall.SIGCONT)
		_ = handoffSignal(cleanup, r.Adapter, r.AdapterBirth, syscall.SIGCONT)
		_, _ = handoffLaunchctl(cleanup, "bootout", target)
		_ = handoffSignal(cleanup, r.Adapter, r.AdapterBirth, syscall.SIGTERM)
		if r.Server > 0 {
			if birth, e := terminalbinding.ProcessBirth(cleanup, r.Server); e == nil && birth == r.ServerBirth {
				device, inode, e := terminalbinding.SocketIdentity(r.Socket)
				if e != nil || device != r.Device || inode != r.Inode {
					t.Error("cannot prove disposable server for cleanup")
					return
				}
				path, _ := exec.LookPath("tmux")
				if _, e := exec.CommandContext(cleanup, path, "-N", "-S", r.Socket, "kill-server").CombinedOutput(); e != nil {
					t.Error(e)
				}
			}
		}
		if err := handoffWait(cleanup, func() bool { return handoffJobAbsent(cleanup, target) }); err != nil {
			t.Error("disposable launchd removal unconfirmed")
			return
		}
		for _, pid := range []int{r.Owner, r.Adapter, r.Server, r.Pane} {
			if pid > 0 {
				if err := handoffWait(cleanup, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }); err != nil {
					t.Errorf("disposable PID remains: %d", pid)
					return
				}
			}
		}
		cleaned = true
	})
	if _, err := handoffLaunchctl(ctx, "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist); err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(root, "ready.json"))
		if err == nil {
			if err := json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("disposable owner did not become ready")
		case <-time.After(20 * time.Millisecond):
		}
	}
	for _, pid := range []int{r.Adapter, r.Server} {
		group, err := syscall.Getpgid(pid)
		if err != nil || group != r.Adapter {
			t.Fatal("fixture does not reproduce shared legacy adapter/server PGID")
		}
	}
	if r.Owner == r.Adapter || filepath.Dir(filepath.Dir(r.Socket)) != root {
		t.Fatal("invalid disposable identity")
	}
	if fenced {
		if err := handoffSignal(ctx, r.Owner, r.OwnerBirth, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		if err := handoffSignal(ctx, r.Adapter, r.AdapterBirth, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		for _, pid := range []int{r.Owner, r.Adapter} {
			if err := handoffWait(ctx, func() bool {
				state, err := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "state=").Output()
				return err == nil && strings.HasPrefix(strings.TrimSpace(string(state)), "T")
			}); err != nil {
				t.Fatal("disposable owner STOP unconfirmed")
			}
		}
	}
	_, stopErr := handoffLaunchctl(ctx, "bootout", target)
	if err := handoffWait(ctx, func() bool { return handoffJobAbsent(ctx, target) }); err != nil {
		t.Fatalf("disposable bootout unconfirmed: %v", stopErr)
	}
	if fenced {
		if err := handoffSignal(ctx, r.Adapter, r.AdapterBirth, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
	}
	for _, pid := range []int{r.Owner, r.Adapter} {
		if err := handoffWait(ctx, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }); err != nil {
			t.Fatal("old disposable owner still exists")
		}
	}
	if err := proveHandoffSurvival(ctx, r); !fenced {
		if err == nil {
			t.Fatal("negative control failed to detect destructive legacy shutdown")
		}
		return
	} else if err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(500 * time.Millisecond); time.Now().Before(until); {
		if err := proveHandoffSurvival(ctx, r); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Log("same server/pane births, socket inode and exact accepted screen survived disposable owner handoff")
}

func handoffWait(ctx context.Context, ready func() bool) error {
	for !ready() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

func handoffJobAbsent(ctx context.Context, target string) bool {
	output, err := handoffLaunchctl(ctx, "print", target)
	return ctx.Err() == nil && err != nil && strings.Contains(string(output), "Could not find service") && strings.Contains(string(output), filepath.Base(target))
}

func handoffLaunchctl(ctx context.Context, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(bounded, "/bin/launchctl", args...).CombinedOutput()
}

func handoffSignal(ctx context.Context, pid int, birth string, sig syscall.Signal) error {
	if pid < 2 || birth == "" {
		return errors.New("disposable process identity missing")
	}
	actual, err := terminalbinding.ProcessBirth(ctx, pid)
	if err != nil || actual != birth {
		return errors.New("disposable process identity changed")
	}
	return syscall.Kill(pid, sig)
}

func proveHandoffSurvival(ctx context.Context, r handoffIdentity) error {
	for _, pair := range []struct {
		pid   int
		birth string
	}{{r.Server, r.ServerBirth}, {r.Pane, r.PaneBirth}} {
		actual, err := terminalbinding.ProcessBirth(ctx, pair.pid)
		if err != nil || actual != pair.birth {
			return errors.New("handoff lost exact live terminal process")
		}
	}
	device, inode, err := terminalbinding.SocketIdentity(r.Socket)
	if err != nil || device != r.Device || inode != r.Inode {
		return errors.New("handoff replaced terminal socket")
	}
	path, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	term := &Terminal{path: path, socket: r.Socket, persistent: true}
	screen, err := term.Capture(ctx)
	if err != nil || screen != r.Screen || strings.Count(screen, "ACCEPTED:disposable-exact-input") != 1 {
		return errors.New("handoff lost or replayed accepted output")
	}
	return nil
}
