//go:build !windows

package main

import (
	"os"
	"syscall"
)

func replaceProcess(binary string) error {
	arguments := append([]string{binary}, os.Args[1:]...)
	return syscall.Exec(binary, arguments, os.Environ())
}
