//go:build linux

package orphanresume

import (
	"fmt"
	"syscall"
	"time"
)

// Cleanup removes an exact provider resume process from a prior instance.
// Empty session IDs or unavailable procfs/workdirs are a no-op.
func Cleanup(sessionID, workdir string) error {
	return cleanup(sessionID, workdir, "/proc", terminateProcessGroup)
}

func terminateProcessGroup(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	if pgid != pid || pgid < 2 {
		return fmt.Errorf("refusing non-dedicated process group %d", pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
