//go:build darwin || linux

package nativeadapter

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// Inherited os.Stdin can be a blocking, non-pollable descriptor: Close does
// not interrupt its in-flight Read. Register an owned nonblocking duplicate
// with Go's poller so cancellation can close and join the scanner goroutine.
func interruptibleInput(input io.ReadCloser) (io.ReadCloser, error) {
	file, ok := input.(*os.File)
	if !ok {
		return input, nil
	}
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, errors.New("native input duplication failed")
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, errors.New("native input polling unavailable")
	}
	return os.NewFile(uintptr(fd), "native-input"), nil
}
