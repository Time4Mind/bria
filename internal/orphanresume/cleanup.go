// Package orphanresume cleans up exact provider resume processes left by a prior instance.
package orphanresume

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cleanup attests ownership using the exact resume ID, canonical working
// directory and a known provider executable name before terminating a group.
func cleanup(sessionID, workdir, procDir string, terminate func(int) error) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	wantID := strings.TrimSpace(sessionID)
	wantDir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return nil // Start will report the canonical workdir error.
	}
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil // procfs is optional; do not make resume less portable.
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 2 || pid == self {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(procDir, entry.Name(), "cmdline"))
		if err != nil || !exactResumeArg(cmdline, wantID) || !knownProviderCLI(cmdline) {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(procDir, entry.Name(), "cwd"))
		if err != nil {
			continue
		}
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil || cwd != wantDir {
			continue
		}
		if err := terminate(pid); err != nil {
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
