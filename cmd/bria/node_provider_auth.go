package main

import (
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/providerauth"
)

func newProviderAuthentication(
	nodeConfig config.Config,
	guard *nodecontrol.StateGuard,
	client *nodecontrol.Client,
) (*providerauth.Manager, *nodecontrol.ProviderAuthRouter, error) {
	launcher, err := providerauth.NewCommandLauncher(map[string]providerauth.Command{
		"claude": {Executable: nodeConfig.ClaudeCommand},
		"codex":  {Executable: nodeConfig.CodexCommand},
	})
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
