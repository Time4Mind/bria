//go:build linux

package sessionruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bria/internal/app"
)

// cleanupOrphanResumeProcess removes only an exact, provider-owned resume
// process left behind by an earlier Bria instance. Provider CLIs do not
// reliably preserve Bria's environment, therefore ownership is attested from
// three independent facts: exact provider session id in argv, exact working
// directory, and a known provider executable name. We signal its dedicated
// process group, never a fuzzy pid or a tmux server.
func cleanupOrphanResumeProcess(request app.StartSessionRequest) error {
	if request.PriorBinding == nil || strings.TrimSpace(request.PriorBinding.SessionID) == "" {
		return nil
	}
	wantID := strings.TrimSpace(request.PriorBinding.SessionID)
	wantDir, err := filepath.EvalSymlinks(request.Workdir)
	if err != nil {
		return nil // Start will report the canonical workdir error.
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil // procfs is optional; do not make resume less portable.
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 2 || pid == self {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || !exactResumeArg(cmdline, wantID) || !knownProviderCLI(cmdline) {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
		if err != nil {
			continue
		}
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil || cwd != wantDir {
			continue
		}
		if err := terminateProcessGroup(pid); err != nil {
			return fmt.Errorf("pid %d: %w", pid, err)
		}
	}
	return nil
}

func exactResumeArg(raw []byte, want string) bool {
	parts := strings.Split(string(raw), "\x00")
	for i, part := range parts {
		if part == "resume" && i+1 < len(parts) && parts[i+1] == want {
			return true
		}
	}
	return false
}

func knownProviderCLI(raw []byte) bool {
	for _, part := range strings.Split(string(raw), "\x00") {
		base := filepath.Base(part)
		if base == "codex" || base == "claude" || base == "codex-cli" || base == "claude-code" {
			return true
		}
	}
	return false
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
