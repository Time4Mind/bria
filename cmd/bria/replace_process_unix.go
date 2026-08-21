//go:build !windows

package main

import (
	"os"
	"syscall"
)

func replaceProcess(binary string) error {
	arguments := append([]string{binary}, os.Args[1:]...)
	identity, err := binarySHA256(binary)
	if err != nil {
		return err
	}
	return syscall.Exec(binary, arguments, environmentWithBinaryIdentity(identity))
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
