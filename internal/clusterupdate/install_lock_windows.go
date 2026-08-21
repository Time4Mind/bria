//go:build windows

package clusterupdate

func installLockOwnerAlive(int) bool {
	// Fail closed on Windows. The supported installers currently run on Unix;
	// an operator can remove a stale Windows lock after verifying ownership.
	return true
}
