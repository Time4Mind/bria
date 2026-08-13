package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
)

func maintainTemporaryLeader(ctx context.Context, node *consensus.Node) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if node.IsLeader() {
			reconcileTemporaryLeader(ctx, node)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func reconcileTemporaryLeader(ctx context.Context, node *consensus.Node) {
	state := node.State().State()
	preference := state.TemporaryLeader
	if preference.NodeID == "" {
		return
	}
	target, exists := state.Nodes[preference.NodeID]
	if !preference.Until.After(time.Now()) || !exists || target.Status == domain.NodeOffline {
		command, err := clusterstate.NewCommand(
			fmt.Sprintf("clear-temporary-leader-%s-%d", preference.NodeID, preference.Until.UnixNano()),
			clusterstate.CommandClearTemporaryLeader, time.Now(), clusterstate.ClearTemporaryLeader{
				NodeID: preference.NodeID, ObservedUntil: preference.Until,
			},
		)
		if err == nil {
			_, _ = node.Apply(ctx, command)
		}
		return
	}
	if node.LeaderID() != string(preference.NodeID) {
		if err := node.TransferLeadershipTo(string(preference.NodeID)); err != nil {
			fmt.Fprintf(os.Stderr, "bria temporary leader: %v\n", err)
		}
	}
}
