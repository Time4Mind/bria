//go:build linux

package orphanresume_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"bria/internal/orphanresume"
)

// Only this test's exact child can match both its unique ID and temporary cwd.
// Teardown also addresses only that child, never a name or a discovered PID.
func TestCleanupPublicProcessBehavior(t *testing.T) {
	for _, tc := range []struct {
		name, provider                             string
		wrongID, wrongDir, sharedGroup, ignoreTerm bool
		wantExit                                   bool
	}{
		{name: "dedicated-group", provider: "codex", wantExit: true},
		{name: "kill-after-grace", provider: "claude", ignoreTerm: true, wantExit: true},
		{name: "other-id", provider: "codex", wrongID: true},
		{name: "other-workdir", provider: "codex", wrongDir: true},
		{name: "unknown-provider", provider: "unrecognized"},
		{name: "shared-group", provider: "codex", sharedGroup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workdir := t.TempDir()
			id := "orphanresume-test-" + filepath.Base(filepath.Dir(workdir))
			command := exec.Command(os.Args[0], "-test.run=^TestOrphanResumeChild$", "--", tc.provider, "resume", id)
			command.Dir = workdir
			command.Env = append(os.Environ(), "BRIA_ORPHAN_RESUME_TEST_CHILD=1")
			if tc.ignoreTerm {
				command.Env = append(command.Env, "BRIA_ORPHAN_RESUME_TEST_IGNORE_TERM=1")
			}
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: !tc.sharedGroup}
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			t.Cleanup(func() {
				_ = command.Process.Kill()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("test child did not exit")
				}
			})
			ready := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(stdout).ReadString('\n')
				ready <- line
			}()
			select {
			case line := <-ready:
				if line != "ready\n" {
					t.Fatalf("child readiness = %q", line)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("test child readiness timeout")
			}
			if tc.wrongID {
				id += "-different"
			}
			if tc.wrongDir {
				workdir = t.TempDir()
			}
			err = orphanresume.Cleanup(id, workdir)
			if tc.sharedGroup {
				if err == nil || !strings.Contains(err.Error(), "refusing non-dedicated process group") {
					t.Fatalf("shared group error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.wantExit {
				select {
				case exitErr := <-done:
					done <- exitErr // Preserve the receipt for teardown.
					exit, ok := exitErr.(*exec.ExitError)
					if !ok {
						t.Fatalf("child exit = %v", exitErr)
					}
					wantSignal := syscall.SIGTERM
					if tc.ignoreTerm {
						wantSignal = syscall.SIGKILL
					}
					if status := exit.Sys().(syscall.WaitStatus); status.Signal() != wantSignal {
						t.Fatalf("child exit signal = %v, want %v", status.Signal(), wantSignal)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not terminate its exact test child")
				}
			} else {
				// Prove the excluded child remains responsive and exits normally.
				if _, err := stdin.Write([]byte("x")); err != nil {
					t.Fatalf("excluded child input: %v", err)
				}
				select {
				case exitErr := <-done:
					done <- exitErr
					if exitErr != nil {
						t.Fatalf("excluded child did not exit normally: %v", exitErr)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("excluded child did not respond")
				}
			}
		})
	}
}

func TestOrphanResumeChild(t *testing.T) {
	if os.Getenv("BRIA_ORPHAN_RESUME_TEST_CHILD") != "1" {
		return
	}
	if os.Getenv("BRIA_ORPHAN_RESUME_TEST_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	fmt.Println("ready")
	_, _ = os.Stdin.Read(make([]byte, 1))
	os.Exit(0)
}
