package providerauth

import (
	"context"
	"errors"
	"strings"
	"time"
)

const startupTimeout = 25 * time.Second

type Command struct {
	Executable string
}

type CommandLauncher struct {
	commands map[string]Command
}

func NewCommandLauncher(commands map[string]Command) (*CommandLauncher, error) {
	copyCommands := make(map[string]Command, len(commands))
	for backend, command := range commands {
		backend = strings.ToLower(strings.TrimSpace(backend))
		command.Executable = strings.TrimSpace(command.Executable)
		if (backend != BackendClaude && backend != BackendCodex) || command.Executable == "" {
			return nil, errors.New("invalid provider authentication command")
		}
		copyCommands[backend] = command
	}
	if len(copyCommands) == 0 {
		return nil, errors.New("provider authentication commands are required")
	}
	return &CommandLauncher{commands: copyCommands}, nil
}

func (l *CommandLauncher) Launch(
	ctx context.Context,
	backend string,
	_ string,
) (Process, error) {
	command, ok := l.commands[backend]
	if !ok {
		return nil, ErrUnsupportedBackend
	}
	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	switch backend {
	case BackendClaude:
		return launchClaude(startupCtx, command.Executable)
	case BackendCodex:
		return launchCodex(startupCtx, command.Executable)
	default:
		return nil, ErrUnsupportedBackend
	}
}

var _ Launcher = (*CommandLauncher)(nil)
