package platform

import (
	"context"
	"errors"
	"os/exec"
)

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	stdout, err := command.Output()
	result := CommandResult{Stdout: stdout}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.Stderr = exitError.Stderr
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}
