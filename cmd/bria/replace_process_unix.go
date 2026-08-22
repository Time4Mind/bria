//go:build !windows

package main

import (
	"os"
	"strings"
	"syscall"
)

func replaceProcess(binary, activation string) error {
	arguments := append([]string{binary}, os.Args[1:]...)
	identity, err := binarySHA256(binary)
	if err != nil {
		return err
	}
	return syscall.Exec(binary, arguments, environmentWithBinaryIdentity(identity, activation))
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
