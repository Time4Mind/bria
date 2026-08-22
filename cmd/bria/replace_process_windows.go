//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
)

func replaceProcess(binary, activation string) error {
	command := exec.Command(binary, os.Args[1:]...)
	identity, err := binarySHA256(binary)
	if err != nil {
		return err
	}
	command.Env = environmentWithBinaryIdentity(identity, activation)
	command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := command.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func environmentWithBinaryIdentity(identity, activation string) []string {
	prefix := expectedBinarySHA256Env + "="
	activationPrefix := providerHookActivationEnv + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, prefix) || strings.HasPrefix(value, activationPrefix) {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, prefix+identity)
	if activation != "" {
		environment = append(environment, activationPrefix+activation)
	}
	return environment
}
