package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/speechsetup"
	"github.com/Time4Mind/bria/internal/systemdeps"
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
	dependencies := systemDependencyConfig(nodeConfig.DataDir)
	inventory := newNodeInventory(
		discoverLocalBackends(ctx, backendRuntime.runner, commands),
		backendRuntime.runner, commands,
	)
	localBackends, err := backendsetup.NewManager(backendsetup.Config{
		NodeID: nodeConfig.NodeID, Roots: managedRoots,
		Commands: map[string]string{"claude": commands.Claude, "codex": commands.Codex},
		Runner:   backendRuntime.runner, Refresh: inventory.Refresh, Dependencies: dependencies,
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
		Engine:  nodeConfig.EffectiveSpeechEngine(),
		DataDir: nodeConfig.DataDir, FFmpegCommand: nodeConfig.FFmpegCommand,
		WhisperCommand: nodeConfig.WhisperCommand, WhisperModel: nodeConfig.WhisperModelPath,
		AppleCommand: nodeConfig.AppleSpeechCommand,
		Dependencies: dependencies,
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

func systemDependencyConfig(dataDir string) systemdeps.Config {
	if runtime.GOOS != "linux" {
		return systemdeps.Config{}
	}
	if info, err := os.Stat("/usr/local/libexec/bria-install-system-deps"); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return systemdeps.Config{}
	}
	return systemdeps.Config{
		RequestDir: filepath.Join(dataDir, "system-deps", "requests"),
		ResultDir:  "/run/bria-system-deps",
	}
}
