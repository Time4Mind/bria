package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/interaction"
)

func confirmRunningUpdate(nodeConfig config.Config) {
	if nodeConfig.UpdateManifestURL == "" {
		return
	}
	installRoot := nodeConfig.EffectiveUpdateInstallRoot()
	if activationPath, err := resolveActivationPath(); err == nil &&
		filepath.Base(filepath.Dir(activationPath)) == "current" && nodeConfig.UpdateInstallRoot == "" {
		installRoot = filepath.Dir(filepath.Dir(activationPath))
	}
	if err := clusterupdate.ConfirmInstalled(installRoot, localBuildVersion()); err != nil {
		fmt.Fprintf(os.Stderr, "bria update confirmation: %v\n", err)
	}
}

func startUpdateCoordinator(
	ctx context.Context, node *consensus.Node, nodeConfig config.Config, control *nodeRuntimeControl,
) (*clusterupdate.Coordinator, error) {
	if control.updates.router == nil {
		return nil, nil
	}
	coordinator, err := clusterupdate.NewCoordinator(
		nodeConfig.NodeID, node.State(), node, control.updates.router,
	)
	if err != nil {
		return nil, err
	}
	go func() { _ = coordinator.Run(ctx, 500*time.Millisecond) }()
	return coordinator, nil
}

func waitForNodeRuntime(
	ctx context.Context, control *nodeRuntimeControl, adapterErrors <-chan interaction.Failure,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case binary := <-control.updates.restarts:
			return &processReplacement{binary: binary}
		case err := <-control.errors:
			if err == nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("node runtime control stopped: %w", err)
		case failure := <-adapterErrors:
			if adapterErrors == nil {
				continue
			}
			if failure.Err == nil || errors.Is(failure.Err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("%s adapter stopped: %w", failure.Adapter, failure.Err)
		}
	}
}
