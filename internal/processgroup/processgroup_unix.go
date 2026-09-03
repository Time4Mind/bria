//go:build darwin || linux

package processgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// Configure prepares cmd for a dedicated process group before cmd.Start.
func Configure(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		return ErrAlreadyStarted
	}

	attributes := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	// Pgid=0 makes the adapter the leader of a new group. Joining a caller-
	// supplied group, foreground manipulation, or combining this with setsid
	// would make the eventual negative-PID signal ambiguous, so fail closed.
	if attributes.Pgid != 0 || attributes.Foreground || attributes.Setsid {
		return ErrUnsafeConfiguration
	}
	attributes.Setpgid = true
	cmd.SysProcAttr = &attributes
	return nil
}

// ConfigureDescendant prepares cmd for safe use by an adapter process. It
// returns true only when the current process is a verified group leader and
// cmd will inherit that group. Otherwise it configures cmd as a new group
// leader and returns false.
func ConfigureDescendant(cmd *exec.Cmd) (bool, error) {
	if cmd == nil {
		return false, ErrInvalidCommand
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		return false, ErrAlreadyStarted
	}

	processID := os.Getpid()
	groupID, err := syscall.Getpgid(processID)
	if err != nil {
		return false, fmt.Errorf("inspect current process group: %w", err)
	}
	if groupID != processID {
		if err := Configure(cmd); err != nil {
			return false, err
		}
		return false, nil
	}

	if attributes := cmd.SysProcAttr; attributes != nil && (attributes.Setpgid || attributes.Pgid != 0 || attributes.Foreground || attributes.Setsid) {
		return false, ErrUnsafeConfiguration
	}
	return true, nil
}

// KillTree terminates the dedicated process group containing cmd. It must be
// called before cmd.Wait; after Wait it is an idempotent no-op because the
// process ID may already have been reused.
func KillTree(cmd *exec.Cmd) error {
	return signalTree(cmd, syscall.SIGKILL)
}

// TerminateTree requests graceful termination of the dedicated process group.
// It must be called before cmd.Wait; after Wait it is an idempotent no-op
// because the process ID may already have been reused.
func TerminateTree(cmd *exec.Cmd) error {
	return signalTree(cmd, syscall.SIGTERM)
}

// ConfirmTreeGone performs a non-signalling post-reap probe of a command's
// dedicated process group. It succeeds only when Wait recorded the leader's
// state and the operating system reports that the original group no longer
// exists. A reused group ID is therefore never signalled and fails closed.
func ConfirmTreeGone(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil || cmd.Process.Pid < 2 {
		return ErrNotStarted
	}
	attributes := cmd.SysProcAttr
	if attributes == nil || !attributes.Setpgid || attributes.Pgid != 0 || attributes.Foreground || attributes.Setsid {
		return ErrUnsafeConfiguration
	}
	if cmd.ProcessState == nil {
		return ErrTreeExitUnconfirmed
	}
	if err := syscall.Kill(-cmd.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	} else if err != nil && !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("probe reaped process group: %w", err)
	}
	return ErrTreeExitUnconfirmed
}

// KillCurrentTree terminates the current executable and its descendants only
// when the executable is the verified leader of its own process group. A
// successful call does not return.
func KillCurrentTree() error {
	processID := os.Getpid()
	groupID, err := syscall.Getpgid(processID)
	if err != nil {
		return fmt.Errorf("inspect current process group: %w", err)
	}
	if groupID != processID {
		return ErrUnsafeConfiguration
	}
	if err := syscall.Kill(-processID, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill current process group: %w", err)
	}
	for {
		runtime.Gosched()
	}
}

func signalTree(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil || cmd.Process.Pid < 2 {
		return ErrNotStarted
	}
	attributes := cmd.SysProcAttr
	if attributes == nil || !attributes.Setpgid || attributes.Pgid != 0 || attributes.Foreground || attributes.Setsid {
		return ErrUnsafeConfiguration
	}

	pid := cmd.Process.Pid
	// os.Process tracks reap safely inside the standard library. Unlike reading
	// exec.Cmd.ProcessState, this check does not race with Cmd.Wait. Callers must
	// still serialize tree signals before Wait to close the signal/reap window.
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		if !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("probe dedicated process: %w", err)
		}
	}
	groupID, err := syscall.Getpgid(pid)
	if err == nil && groupID != pid {
		return ErrUnsafeConfiguration
	}
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("inspect dedicated process group: %w", err)
	}
	if err := syscall.Kill(-pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal dedicated process group: %w", err)
	}
	return nil
}
