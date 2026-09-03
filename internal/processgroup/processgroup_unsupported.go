//go:build !darwin && !linux && !windows

package processgroup

import "os/exec"

// Configure fails closed on platforms without a verified tree-isolation
// implementation.
func Configure(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		return ErrAlreadyStarted
	}
	return ErrUnsupported
}

// ConfigureDescendant fails closed on unverified platforms.
func ConfigureDescendant(cmd *exec.Cmd) (bool, error) {
	if cmd == nil {
		return false, ErrInvalidCommand
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		return false, ErrAlreadyStarted
	}
	return false, ErrUnsupported
}

// KillTree fails closed on platforms without a verified tree-kill primitive.
func KillTree(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil {
		return ErrNotStarted
	}
	return ErrUnsupported
}

// TerminateTree fails closed on platforms without a verified tree-signal
// primitive.
func TerminateTree(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil {
		return ErrNotStarted
	}
	return ErrUnsupported
}

// ConfirmTreeGone fails closed on platforms without verified tree isolation.
func ConfirmTreeGone(cmd *exec.Cmd) error {
	if cmd == nil {
		return ErrInvalidCommand
	}
	if cmd.Process == nil {
		return ErrNotStarted
	}
	return ErrUnsupported
}

// KillCurrentTree fails closed on unverified platforms.
func KillCurrentTree() error {
	return ErrUnsupported
}
