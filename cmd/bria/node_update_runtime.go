package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/interaction"
	"github.com/Time4Mind/bria/internal/processlog"
)

func confirmRunningUpdate(nodeConfig config.Config, manager *clusterupdate.Manager) {
	if nodeConfig.UpdateManifestURL == "" || manager == nil {
		return
	}
	if err := manager.ConfirmInstalled(localBuildVersion()); err != nil {
		processlog.Failuref(
			processlog.Critical, processlog.FailureConsistency,
			"bria update confirmation: outcome=failed",
		)
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
