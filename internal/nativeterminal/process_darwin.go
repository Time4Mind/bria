//go:build darwin

package nativeterminal

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// macOS has no Linux pidfd equivalent in the supported deployment baseline.
// tmux creates the pane in its own process group, so group-scoped termination
// still avoids touching unrelated processes while preserving native startup.
type ownedProcess struct{ pid int }

func pauseServer(*exec.Cmd) (func(), error) { return func() {}, nil }

func ownProcess(pid int) (*ownedProcess, error) {
	if pid < 2 {
		return nil, errors.New("invalid owned pane PID")
	}
	return &ownedProcess{pid: pid}, nil
}

func (p *ownedProcess) killTree() error {
	if p == nil || p.pid < 2 {
		return errors.New("owned process missing")
	}
	// Kill the pane's process group first, then the exact pane PID as a fallback.
	if err := syscall.Kill(-p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	process, err := os.FindProcess(p.pid)
	if err == nil {
		_ = process.Kill()
	}
	return nil
}
