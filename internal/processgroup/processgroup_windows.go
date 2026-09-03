//go:build windows

package processgroup

import "os/exec"

// Configure fails closed on Windows. The Go standard library does not expose
// a Job Object lifecycle that can guarantee descendant cleanup, and falling
// back to Process.Kill would leave provider descendants running.
func Configure(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		return ErrAlreadyStarted
	}
	return ErrUnsupported
}

// ConfigureDescendant fails closed because Windows tree isolation is not
// available through this package's standard-library implementation.
func ConfigureDescendant(cmd *exec.Cmd) (bool, error) {
	if cmd == nil {
		return false, ErrInvalidCommand
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		return false, ErrAlreadyStarted
	}
	return false, ErrUnsupported
}

// KillTree fails closed on Windows rather than claiming that killing only the
// adapter process also terminated its descendants.
func KillTree(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil {
		return ErrNotStarted
	}
	return ErrUnsupported
}

// TerminateTree fails closed on Windows for the same reason as KillTree.
func TerminateTree(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil {
		return ErrNotStarted
	}
	return ErrUnsupported
}

// ConfirmTreeGone fails closed because Windows tree isolation is unsupported.
func ConfirmTreeGone(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil {
		return ErrNotStarted
	}
	return ErrUnsupported
}

// KillCurrentTree fails closed on Windows.
func KillCurrentTree() error {
	return ErrUnsupported
}
