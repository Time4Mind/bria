//go:build windows

package main

import "errors"

func reexecRunnerActivation() error {
	return errors.New("runner activation re-exec is unavailable on Windows")
}
