package main

import (
	"context"

	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/interaction"
)

func startInteractionAdapters(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
	runtimeControl *nodeRuntimeControl,
	updateCoordinator *clusterupdate.Coordinator,
) (<-chan interaction.Failure, error) {
	adapters := make([]interaction.Adapter, 0, 1)
	telegram, err := newTelegramAdapter(
		ctx, node, nodeConfig, runtimeControl, updateCoordinator,
	)
	if err != nil {
		return nil, err
	}
	if telegram != nil {
		adapters = append(adapters, telegram)
	}
	return interaction.Start(ctx, adapters...), nil
}
