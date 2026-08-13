package inbound

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

type CommandRunner interface {
	Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args ...string,
) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return &CommandError{Binary: name, ExitCode: exitError.ExitCode()}
	}
	return err
}

type CommandError struct {
	Binary   string
	ExitCode int
}

func (e *CommandError) Error() string {
	return e.Binary + " exited unsuccessfully"
}

type truncatingBuffer struct {
	data  []byte
	limit int
}

func (w *truncatingBuffer) Write(data []byte) (int, error) {
	available := w.limit - len(w.data)
	if available > len(data) {
		available = len(data)
	}
	if available > 0 {
		w.data = append(w.data, data[:available]...)
	}
	return len(data), nil
}

func (w *truncatingBuffer) String() string {
	return string(w.data)
}
