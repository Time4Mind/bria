//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func reexecRunnerActivation() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	activation, _ := runnerActivationPaths(executable)
	if activation == "" {
		return errors.New("runner activation path is unavailable")
	}
	target := filepath.Join(activation, filepath.Base(executable))
	return syscall.Exec(target, os.Args, os.Environ())
}
