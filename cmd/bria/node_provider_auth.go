package main

import (
	"fmt"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/runnerhost"
)

func newProviderAuthentication(
	nodeConfig config.Config,
	guard *nodecontrol.StateGuard,
	client *nodecontrol.Client,
	backendRuntime backendRuntime,
) (*providerauth.Manager, *nodecontrol.ProviderAuthRouter, error) {
	commands := map[string]providerauth.Command{
		"claude": {Executable: nodeConfig.ClaudeCommand},
		"codex":  {Executable: nodeConfig.CodexCommand},
	}
	var launcher providerauth.Launcher
	var err error
	if nodeConfig.IsolatedRunner() {
		runnerClient, ok := backendRuntime.runner.(*runnerhost.Client)
		if !ok {
			return nil, nil, fmt.Errorf("isolated provider authentication runner is unavailable")
		}
		launcher, err = runnerhost.NewAuthLauncher(runnerClient, commands)
	} else {
		launcher, err = providerauth.NewCommandLauncher(commands)
	}
	if err != nil {
		return nil, nil, err
	}
	local, err := providerauth.NewManager(nodeConfig.NodeID, guard, launcher)
	if err != nil {
		return nil, nil, err
	}
	remote, err := nodecontrol.NewProviderAuthClient(client)
	if err != nil {
		return nil, nil, err
	}
	router, err := nodecontrol.NewProviderAuthRouter(nodeConfig.NodeID, local, remote)
	if err != nil {
		_ = local.Close()
		return nil, nil, err
	}
	return local, router, nil
}
