//go:build windows

package main

import (
	"os"
	"os/exec"
)

func replaceProcess(binary string) error {
	command := exec.Command(binary, os.Args[1:]...)
	command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := command.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
