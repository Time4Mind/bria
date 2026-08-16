package main

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

const runnerActivationPollInterval = 2 * time.Second

// watchRunnerActivation lets a rootless isolated runner follow cluster
// updates.  The node cannot signal a process owned by the runner uid, so the
// runner observes the stable "current" symlink and exits when it points at a
// different release.  systemd then restarts it from the new target.
func watchRunnerActivation(ctx context.Context) <-chan struct{} {
	changed := make(chan struct{})
	executable, err := os.Executable()
	activation, current := runnerActivationPaths(executable)
	if err != nil || activation == "" {
		return changed
	}
	go func() {
		ticker := time.NewTicker(runnerActivationPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				target, resolveErr := filepath.EvalSymlinks(activation)
				if resolveErr == nil && filepath.Clean(target) != current {
					close(changed)
					return
				}
			}
		}
	}()
	return changed
}

func runnerActivationPaths(executable string) (activation, current string) {
	executable = filepath.Clean(executable)
	release := filepath.Dir(executable)
	releases := filepath.Dir(release)
	if filepath.Base(releases) != "releases" || filepath.Base(executable) == "." {
		return "", ""
	}
	return filepath.Join(filepath.Dir(releases), "current"), release
}
