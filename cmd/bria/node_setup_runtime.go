package main

import (
	"context"
	"runtime"

	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/speechsetup"
)

type nodeSetupRuntime struct {
	localBackends *backendsetup.Manager
	backends      *nodecontrol.BackendSetupRouter
	localSpeech   *speechsetup.Manager
	speech        *nodecontrol.SpeechSetupRouter
	inventory     *nodeInventory
}

func newNodeSetupRuntime(
	ctx context.Context,
	nodeConfig config.Config,
	managedRoots map[string]string,
	backendRuntime backendRuntime,
	client *nodecontrol.Client,
) (nodeSetupRuntime, error) {
	commands := configuredBackendCommands(nodeConfig)
	inventory := newNodeInventory(
		discoverLocalBackends(ctx, backendRuntime.runner, commands),
		backendRuntime.runner, commands,
	)
	localBackends, err := backendsetup.NewManager(backendsetup.Config{
		NodeID: nodeConfig.NodeID, Roots: managedRoots,
		Commands: map[string]string{"claude": commands.Claude, "codex": commands.Codex},
		Runner:   backendRuntime.runner, Refresh: inventory.Refresh,
	})
	if err != nil {
		return nodeSetupRuntime{}, err
	}
	remoteBackends, err := nodecontrol.NewBackendSetupClient(client)
	if err != nil {
		return nodeSetupRuntime{}, err
	}
	backends, err := nodecontrol.NewBackendSetupRouter(
		nodeConfig.NodeID, localBackends, remoteBackends,
	)
	if err != nil {
		return nodeSetupRuntime{}, err
	}
	localSpeech, err := speechsetup.NewManager(speechsetup.Config{
		NodeID: nodeConfig.NodeID, OS: runtime.GOOS, Arch: runtime.GOARCH,
		DataDir: nodeConfig.DataDir, FFmpegCommand: nodeConfig.FFmpegCommand,
		WhisperCommand: nodeConfig.WhisperCommand, WhisperModel: nodeConfig.WhisperModelPath,
		AppleCommand: nodeConfig.AppleSpeechCommand,
	})
	if err != nil {
		return nodeSetupRuntime{}, err
	}
	remoteSpeech, err := nodecontrol.NewSpeechSetupClient(client)
	if err != nil {
		return nodeSetupRuntime{}, err
	}
	speech, err := nodecontrol.NewSpeechSetupRouter(nodeConfig.NodeID, localSpeech, remoteSpeech)
	if err != nil {
		return nodeSetupRuntime{}, err
	}
	return nodeSetupRuntime{
		localBackends: localBackends, backends: backends,
		localSpeech: localSpeech, speech: speech, inventory: inventory,
	}, nil
}
