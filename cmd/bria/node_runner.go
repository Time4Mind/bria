package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/runnerhost"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessionname"
)

type backendRuntime struct {
	runner     runtimehost.JSONRPCCommandRunner
	nameRunner sessionname.Runner
	home       string
	closer     io.Closer
}

func openBackendRuntime(ctx context.Context, nodeConfig config.Config) (backendRuntime, error) {
	if !nodeConfig.IsolatedRunner() {
		home, err := os.UserHomeDir()
		if err != nil {
			return backendRuntime{}, fmt.Errorf("resolve runtime home: %w", err)
		}
		return backendRuntime{
			runner: runtimehost.ExecCommandRunner{}, nameRunner: sessionname.ExecRunner{}, home: home,
			closer: noOpCloser{},
		}, nil
	}
	if runtime.GOOS != "linux" {
		return backendRuntime{}, errors.New("isolated runners are supported only on Linux hosts")
	}
	client, err := runnerhost.NewClient(nodeConfig.Runner.Socket)
	if err != nil {
		return backendRuntime{}, err
	}
	socketOwner, err := runnerhost.SocketOwnerUID(nodeConfig.Runner.Socket)
	if err != nil {
		_ = client.Close()
		return backendRuntime{}, err
	}
	control := runnerhost.LocalInspect()
	if socketOwner == 0 || socketOwner == control.UID {
		_ = client.Close()
		return backendRuntime{}, errors.New("runner socket must be owned by a separate non-root user")
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	remote, err := client.Inspect(inspectCtx)
	if err != nil {
		_ = client.Close()
		return backendRuntime{}, fmt.Errorf("inspect isolated runner: %w", err)
	}
	if err := validateRunnerIsolation(nodeConfig.EffectiveRunnerMode(), control, remote); err != nil {
		_ = client.Close()
		return backendRuntime{}, err
	}
	return backendRuntime{
		runner: client, nameRunner: commandNameRunner{runner: client},
		home: nodeConfig.Runner.Home, closer: client,
	}, nil
}

func validateRunnerIsolation(mode string, control, runner runnerhost.Inspect) error {
	if runner.OS != "linux" || runner.UID == 0 {
		return errors.New("isolated runner must be a non-root Linux process")
	}
	if control.UID >= 0 && runner.UID == control.UID && mode != config.RunnerModeDocker {
		return errors.New("isolated runner must use a different user from the control process")
	}
	switch mode {
	case config.RunnerModeDocker:
		if !runner.Container {
			return errors.New("docker runner is not running inside a container")
		}
		if control.MountNamespace != "" && runner.MountNamespace == control.MountNamespace {
			return errors.New("docker runner shares the control process mount namespace")
		}
	case config.RunnerModeNativeUser:
		if runner.Container || runner.WSL {
			return errors.New("native-user runner must be a native Linux process")
		}
	case config.RunnerModeWSL:
		if !runner.WSL {
			return errors.New("wsl runner is not running under WSL")
		}
		if runner.WindowsInterop || runner.WindowsMounts {
			return errors.New("wsl runner requires Windows interop and Windows drive mounts to be disabled")
		}
	default:
		return errors.New("unknown isolated runner mode")
	}
	return nil
}

type commandNameRunner struct{ runner runtimehost.CommandRunner }

func (r commandNameRunner) Run(
	ctx context.Context,
	_ []string,
	name string,
	args ...string,
) ([]byte, []byte, int, error) {
	result, err := r.runner.Run(ctx, name, args...)
	return result.Stdout, result.Stderr, result.ExitCode, err
}

type noOpCloser struct{}

func (noOpCloser) Close() error { return nil }

func isolationSummary(mode string, inspect runnerhost.Inspect) string {
	properties := []string{mode, fmt.Sprintf("uid=%d", inspect.UID), inspect.OS + "/" + inspect.Arch}
	if inspect.Container {
		properties = append(properties, "container")
	}
	if inspect.WSL {
		properties = append(properties, "wsl")
	}
	return strings.Join(properties, " ")
}
