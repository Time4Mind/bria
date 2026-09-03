//go:build darwin || linux

package processgroup_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"bria/internal/processgroup"
)

const (
	helperModeEnvironment   = "BRIA_PROCESSGROUP_HELPER"
	helperReadyEnvironment  = "BRIA_PROCESSGROUP_READY"
	helperParentTerminated  = "BRIA_PROCESSGROUP_PARENT_TERMINATED"
	helperChildTerminated   = "BRIA_PROCESSGROUP_CHILD_TERMINATED"
	helperResultEnvironment = "BRIA_PROCESSGROUP_RESULT"
)

func TestConfiguredCommandAndGrandchildAreKilledAsOneTree(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	readyPath := filepath.Join(t.TempDir(), "grandchild.pid")
	command := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
	command.Env = append(os.Environ(),
		helperModeEnvironment+"=parent",
		helperReadyEnvironment+"="+readyPath,
	)
	if err := processgroup.Configure(command); err != nil {
		t.Fatalf("configure process group: %v", err)
	}
	if err := processgroup.Configure(command); err != nil {
		t.Fatalf("configure process group idempotently: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start configured parent: %v", err)
	}
	terminated := false
	t.Cleanup(func() {
		if !terminated {
			_ = processgroup.KillTree(command)
			_ = command.Wait()
		}
	})

	grandchildPID := waitForPID(t, readyPath, 3*time.Second)
	if err := syscall.Kill(command.Process.Pid, 0); err != nil {
		t.Fatalf("parent exited after publishing grandchild readiness: %v", err)
	}
	if err := syscall.Kill(grandchildPID, 0); err != nil {
		t.Fatalf("grandchild exited after publishing readiness: %v", err)
	}
	parentGroup, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		t.Fatalf("read parent process group: %v", err)
	}
	grandchildGroup, err := syscall.Getpgid(grandchildPID)
	if err != nil {
		t.Fatalf("read grandchild process group: %v", err)
	}
	if parentGroup != command.Process.Pid {
		t.Fatalf("parent process group = %d, want dedicated group %d", parentGroup, command.Process.Pid)
	}
	if grandchildGroup != parentGroup {
		t.Fatalf("grandchild process group = %d, want parent group %d", grandchildGroup, parentGroup)
	}

	if err := processgroup.KillTree(command); err != nil {
		t.Fatalf("kill process tree: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed parent exited successfully, want signal exit")
	}
	terminated = true
	if err := processgroup.KillTree(command); err != nil {
		t.Fatalf("kill already terminated tree idempotently: %v", err)
	}
	waitForProcessGroupGone(t, parentGroup, 3*time.Second)
}

func TestConfigureAndKillTreeFailClosedForInvalidCommands(t *testing.T) {
	if err := processgroup.Configure(nil); !errors.Is(err, processgroup.ErrInvalidCommand) {
		t.Fatalf("Configure(nil) = %v, want %v", err, processgroup.ErrInvalidCommand)
	}
	if err := processgroup.KillTree(nil); !errors.Is(err, processgroup.ErrInvalidCommand) {
		t.Fatalf("KillTree(nil) = %v, want %v", err, processgroup.ErrInvalidCommand)
	}
	if err := processgroup.TerminateTree(nil); !errors.Is(err, processgroup.ErrInvalidCommand) {
		t.Fatalf("TerminateTree(nil) = %v, want %v", err, processgroup.ErrInvalidCommand)
	}
	if inherited, err := processgroup.ConfigureDescendant(nil); inherited || !errors.Is(err, processgroup.ErrInvalidCommand) {
		t.Fatalf("ConfigureDescendant(nil) = (%v, %v), want (false, %v)", inherited, err, processgroup.ErrInvalidCommand)
	}
	unstarted := exec.Command("unused")
	if err := processgroup.KillTree(unstarted); !errors.Is(err, processgroup.ErrNotStarted) {
		t.Fatalf("KillTree(unstarted) = %v, want %v", err, processgroup.ErrNotStarted)
	}
	if err := processgroup.TerminateTree(unstarted); !errors.Is(err, processgroup.ErrNotStarted) {
		t.Fatalf("TerminateTree(unstarted) = %v, want %v", err, processgroup.ErrNotStarted)
	}
	unsafe := exec.Command("unused")
	unsafe.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: 42}
	if err := processgroup.Configure(unsafe); !errors.Is(err, processgroup.ErrUnsafeConfiguration) {
		t.Fatalf("Configure(preassigned group) = %v, want %v", err, processgroup.ErrUnsafeConfiguration)
	}
}

func TestTerminateTreeReachesParentAndGrandchildHandlers(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	root := t.TempDir()
	readyPath := filepath.Join(root, "terminate-grandchild.pid")
	parentReceipt := filepath.Join(root, "parent-terminated")
	childReceipt := filepath.Join(root, "child-terminated")
	command := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
	command.Env = append(os.Environ(),
		helperModeEnvironment+"=terminate-parent",
		helperReadyEnvironment+"="+readyPath,
		helperParentTerminated+"="+parentReceipt,
		helperChildTerminated+"="+childReceipt,
	)
	if err := processgroup.Configure(command); err != nil {
		t.Fatalf("configure process group: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start configured parent: %v", err)
	}
	finished := false
	t.Cleanup(func() {
		if !finished {
			_ = processgroup.KillTree(command)
			_ = command.Wait()
		}
	})

	grandchildPID := waitForPID(t, readyPath, 3*time.Second)
	assertDedicatedTree(t, command.Process.Pid, grandchildPID)
	if err := processgroup.TerminateTree(command); err != nil {
		t.Fatalf("terminate process tree: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("SIGTERM handlers did not exit cleanly: %v", err)
	}
	finished = true
	waitForFile(t, parentReceipt, 3*time.Second)
	waitForFile(t, childReceipt, 3*time.Second)
	if err := processgroup.TerminateTree(command); err != nil {
		t.Fatalf("terminate already exited tree idempotently: %v", err)
	}
	waitForProcessGroupGone(t, command.Process.Pid, 3*time.Second)
}

func TestRunningUnconfiguredCommandIsNeverTreatedAsAKillableTree(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	command := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
	command.Env = append(os.Environ(), helperModeEnvironment+"=grandchild")
	if err := command.Start(); err != nil {
		t.Fatalf("start unconfigured command: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	if err := processgroup.Configure(command); !errors.Is(err, processgroup.ErrAlreadyStarted) {
		t.Fatalf("Configure(started) = %v, want %v", err, processgroup.ErrAlreadyStarted)
	}
	if err := processgroup.KillTree(command); !errors.Is(err, processgroup.ErrUnsafeConfiguration) {
		t.Fatalf("KillTree(unconfigured) = %v, want %v", err, processgroup.ErrUnsafeConfiguration)
	}
	if err := processgroup.TerminateTree(command); !errors.Is(err, processgroup.ErrUnsafeConfiguration) {
		t.Fatalf("TerminateTree(unconfigured) = %v, want %v", err, processgroup.ErrUnsafeConfiguration)
	}
}

func TestReapedCommandIsAnIdempotentNoOp(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	reaped := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
	reaped.Env = append(os.Environ(), helperModeEnvironment+"=exit")
	if err := processgroup.Configure(reaped); err != nil {
		t.Fatalf("configure short-lived command: %v", err)
	}
	if err := reaped.Run(); err != nil {
		t.Fatalf("run short-lived command: %v", err)
	}
	if reaped.ProcessState == nil {
		t.Fatal("short-lived command has no reaped process state")
	}
	if err := reaped.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("probe reaped process = %v, want %v", err, os.ErrProcessDone)
	}
	if err := processgroup.TerminateTree(reaped); err != nil {
		t.Fatalf("terminate reaped command: %v", err)
	}
	if err := processgroup.KillTree(reaped); err != nil {
		t.Fatalf("kill reaped command: %v", err)
	}
}

func TestConfigureDescendantInheritsOnlyFromVerifiedGroupLeader(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	t.Run("leader inheritance", func(t *testing.T) {
		readyPath := filepath.Join(t.TempDir(), "inherited-grandchild.pid")
		command := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		command.Env = append(os.Environ(),
			helperModeEnvironment+"=descendant-parent",
			helperReadyEnvironment+"="+readyPath,
		)
		if err := processgroup.Configure(command); err != nil {
			t.Fatalf("configure leader: %v", err)
		}
		if err := command.Start(); err != nil {
			t.Fatalf("start leader: %v", err)
		}
		t.Cleanup(func() {
			_ = processgroup.KillTree(command)
			_ = command.Wait()
		})

		grandchildPID := waitForPID(t, readyPath, 3*time.Second)
		assertDedicatedTree(t, command.Process.Pid, grandchildPID)
	})

	t.Run("non-leader fallback", func(t *testing.T) {
		readyPath := filepath.Join(t.TempDir(), "isolated-grandchild.pid")
		command := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		command.Env = append(os.Environ(),
			helperModeEnvironment+"=descendant-fallback-parent",
			helperReadyEnvironment+"="+readyPath,
		)
		if err := command.Start(); err != nil {
			t.Fatalf("start non-leader parent: %v", err)
		}
		grandchildPID := 0
		t.Cleanup(func() {
			if grandchildPID > 1 {
				_ = syscall.Kill(-grandchildPID, syscall.SIGKILL)
			}
			_ = command.Process.Kill()
			_ = command.Wait()
		})

		grandchildPID = waitForPID(t, readyPath, 3*time.Second)
		parentGroup, err := syscall.Getpgid(command.Process.Pid)
		if err != nil {
			t.Fatalf("read non-leader parent group: %v", err)
		}
		if parentGroup == command.Process.Pid {
			t.Fatalf("fallback parent unexpectedly leads group %d", parentGroup)
		}
		grandchildGroup, err := syscall.Getpgid(grandchildPID)
		if err != nil {
			t.Fatalf("read isolated grandchild group: %v", err)
		}
		if grandchildGroup != grandchildPID {
			t.Fatalf("fallback grandchild group = %d, want own group %d", grandchildGroup, grandchildPID)
		}
		if grandchildGroup == parentGroup {
			t.Fatalf("fallback grandchild remained in caller group %d", parentGroup)
		}
	})
}

func TestKillCurrentTreeKillsInheritedDescendantAndRejectsNonLeader(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	t.Run("verified leader", func(t *testing.T) {
		readyPath := filepath.Join(t.TempDir(), "current-tree-grandchild.pid")
		command := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		command.Env = append(os.Environ(),
			helperModeEnvironment+"=current-tree-parent",
			helperReadyEnvironment+"="+readyPath,
		)
		if err := processgroup.Configure(command); err != nil {
			t.Fatalf("configure current-tree helper: %v", err)
		}
		if err := command.Start(); err != nil {
			t.Fatalf("start current-tree helper: %v", err)
		}
		finished := false
		t.Cleanup(func() {
			if !finished {
				_ = processgroup.KillTree(command)
				_ = command.Wait()
			}
		})

		grandchildPID := waitForPID(t, readyPath, 3*time.Second)
		if err := command.Wait(); err == nil {
			t.Fatal("KillCurrentTree helper exited successfully, want SIGKILL")
		}
		finished = true
		waitForProcessGroupGone(t, command.Process.Pid, 3*time.Second)
		if err := syscall.Kill(grandchildPID, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("inherited grandchild still exists after current-tree kill: %v", err)
		}
	})

	t.Run("non-leader", func(t *testing.T) {
		resultPath := filepath.Join(t.TempDir(), "non-leader-safe")
		command := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		command.Env = append(os.Environ(),
			helperModeEnvironment+"=current-tree-safety-parent",
			helperResultEnvironment+"="+resultPath,
		)
		if err := processgroup.Configure(command); err != nil {
			t.Fatalf("configure safety group: %v", err)
		}
		if err := command.Run(); err != nil {
			t.Fatalf("non-leader safety group was signalled: %v", err)
		}
		waitForFile(t, resultPath, 3*time.Second)
	})
}

func TestProcessGroupHelper(t *testing.T) {
	switch os.Getenv(helperModeEnvironment) {
	case "":
		t.Skip("helper process entrypoint")
	case "parent":
		testBinary, err := os.Executable()
		if err != nil {
			os.Exit(21)
		}
		grandchild := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		grandchild.Env = append(os.Environ(), helperModeEnvironment+"=grandchild")
		if err := grandchild.Start(); err != nil {
			os.Exit(22)
		}
		if err := os.WriteFile(
			os.Getenv(helperReadyEnvironment),
			[]byte(strconv.Itoa(grandchild.Process.Pid)),
			0o600,
		); err != nil {
			os.Exit(23)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "terminate-parent":
		termination := make(chan os.Signal, 1)
		signal.Notify(termination, syscall.SIGTERM)
		defer signal.Stop(termination)
		testBinary, err := os.Executable()
		if err != nil {
			os.Exit(25)
		}
		grandchild := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		grandchild.Env = append(os.Environ(), helperModeEnvironment+"=terminate-grandchild")
		if err := grandchild.Start(); err != nil {
			os.Exit(26)
		}
		<-termination
		if err := os.WriteFile(os.Getenv(helperParentTerminated), []byte("SIGTERM"), 0o600); err != nil {
			os.Exit(27)
		}
		if err := grandchild.Wait(); err != nil {
			os.Exit(28)
		}
	case "terminate-grandchild":
		termination := make(chan os.Signal, 1)
		signal.Notify(termination, syscall.SIGTERM)
		defer signal.Stop(termination)
		if err := os.WriteFile(
			os.Getenv(helperReadyEnvironment),
			[]byte(strconv.Itoa(os.Getpid())),
			0o600,
		); err != nil {
			os.Exit(29)
		}
		<-termination
		if err := os.WriteFile(os.Getenv(helperChildTerminated), []byte("SIGTERM"), 0o600); err != nil {
			os.Exit(30)
		}
	case "descendant-parent", "descendant-fallback-parent":
		testBinary, err := os.Executable()
		if err != nil {
			os.Exit(31)
		}
		grandchild := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		grandchild.Env = append(os.Environ(), helperModeEnvironment+"=grandchild")
		inherited, err := processgroup.ConfigureDescendant(grandchild)
		wantInherited := os.Getenv(helperModeEnvironment) == "descendant-parent"
		if err != nil || inherited != wantInherited {
			os.Exit(32)
		}
		if err := grandchild.Start(); err != nil {
			os.Exit(33)
		}
		if err := os.WriteFile(
			os.Getenv(helperReadyEnvironment),
			[]byte(strconv.Itoa(grandchild.Process.Pid)),
			0o600,
		); err != nil {
			os.Exit(34)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "current-tree-parent":
		testBinary, err := os.Executable()
		if err != nil {
			os.Exit(35)
		}
		grandchild := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		grandchild.Env = append(os.Environ(), helperModeEnvironment+"=current-tree-grandchild")
		inherited, err := processgroup.ConfigureDescendant(grandchild)
		if err != nil || !inherited {
			os.Exit(36)
		}
		if err := grandchild.Start(); err != nil {
			os.Exit(37)
		}
		if err := waitForPath(os.Getenv(helperReadyEnvironment), 3*time.Second); err != nil {
			os.Exit(38)
		}
		if err := processgroup.KillCurrentTree(); err != nil {
			os.Exit(39)
		}
		os.Exit(40)
	case "current-tree-grandchild":
		if err := os.WriteFile(
			os.Getenv(helperReadyEnvironment),
			[]byte(strconv.Itoa(os.Getpid())),
			0o600,
		); err != nil {
			os.Exit(41)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "current-tree-safety-parent":
		testBinary, err := os.Executable()
		if err != nil {
			os.Exit(42)
		}
		child := exec.Command(testBinary, "-test.run=^TestProcessGroupHelper$")
		child.Env = append(os.Environ(), helperModeEnvironment+"=current-tree-safety-child")
		inherited, err := processgroup.ConfigureDescendant(child)
		if err != nil || !inherited {
			os.Exit(43)
		}
		if err := child.Run(); err != nil {
			os.Exit(44)
		}
	case "current-tree-safety-child":
		if err := processgroup.KillCurrentTree(); !errors.Is(err, processgroup.ErrUnsafeConfiguration) {
			os.Exit(45)
		}
		if err := os.WriteFile(os.Getenv(helperResultEnvironment), []byte("safe"), 0o600); err != nil {
			os.Exit(46)
		}
	case "grandchild":
		for {
			time.Sleep(time.Hour)
		}
	case "exit":
		return
	default:
		os.Exit(24)
	}
}

func assertDedicatedTree(t *testing.T, parentPID, grandchildPID int) {
	t.Helper()
	if err := syscall.Kill(parentPID, 0); err != nil {
		t.Fatalf("parent exited after publishing grandchild readiness: %v", err)
	}
	if err := syscall.Kill(grandchildPID, 0); err != nil {
		t.Fatalf("grandchild exited after publishing readiness: %v", err)
	}
	parentGroup, err := syscall.Getpgid(parentPID)
	if err != nil {
		t.Fatalf("read parent process group: %v", err)
	}
	grandchildGroup, err := syscall.Getpgid(grandchildPID)
	if err != nil {
		t.Fatalf("read grandchild process group: %v", err)
	}
	if parentGroup != parentPID {
		t.Fatalf("parent process group = %d, want dedicated group %d", parentGroup, parentPID)
	}
	if grandchildGroup != parentGroup {
		t.Fatalf("grandchild process group = %d, want parent group %d", grandchildGroup, parentGroup)
	}
}

func waitForPID(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(string(encoded))
			if err != nil || pid < 1 {
				t.Fatalf("decode grandchild pid %q: %v", encoded, err)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read grandchild pid: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grandchild pid did not appear within %s", timeout)
	return 0
}

func waitForProcessGroupGone(t *testing.T, groupID int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(-groupID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("probe process group %d: %v", groupID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process group %d still exists after %s", groupID, timeout)
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	if err := waitForPath(path, timeout); err != nil {
		t.Fatal(err)
	}
}

func waitForPath(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat receipt %q: %w", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("receipt %q did not appear within %s", path, timeout)
}
