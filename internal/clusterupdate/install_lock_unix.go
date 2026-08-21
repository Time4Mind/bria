//go:build !windows

package clusterupdate

import (
	"errors"
	"syscall"
)

func installLockOwnerAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
