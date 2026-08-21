//go:build windows

package main

import (
	"os"
	"os/exec"
)

func replaceProcess(binary string) error {
	command := exec.Command(binary, os.Args[1:]...)
	identity, err := binarySHA256(binary)
	if err != nil {
		return err
	}
	command.Env = environmentWithBinaryIdentity(identity)
	command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := command.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func environmentWithBinaryIdentity(identity string) []string {
	const prefix = expectedBinarySHA256Env + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, prefix+identity)
}
