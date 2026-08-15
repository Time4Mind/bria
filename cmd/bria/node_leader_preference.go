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

func maintainLeaderPolicy(ctx context.Context, node *consensus.Node) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if node.IsLeader() {
			reconcileLeaderPolicy(ctx, node)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func reconcileLeaderPolicy(ctx context.Context, node *consensus.Node) {
	state := node.State().State()
	if state.LeaderPolicy.EffectiveMode() == domain.LeaderSelectionManual {
		targetID := state.LeaderPolicy.NodeID
		if targetID == "" || node.LeaderID() == string(targetID) {
			return
		}
		target, exists := state.Nodes[targetID]
		if !exists || !target.Enabled() || target.Status == domain.NodeOffline {
			return
		}
		if err := node.TransferLeadershipTo(string(targetID)); err != nil {
			fmt.Fprintf(os.Stderr, "bria preferred leader: %v\n", err)
		}
		return
	}
	reconcileTemporaryLeader(ctx, node)
}

// adapterLeadership gates user-facing adapters and their background work. An
// unassigned manual policy temporarily follows the consensus leader so the
// owner can complete first-run leader selection. Once assigned, only that node
// may expose an adapter; other consensus members wait for it to return.
type adapterLeadership struct {
	nodeID domain.NodeID
	node   *consensus.Node
}

func (l adapterLeadership) IsLeader() bool {
	if l.node == nil || !l.node.IsLeader() {
		return false
	}
	policy := l.node.State().State().LeaderPolicy
	return adapterLeadershipAllowed(l.nodeID, domain.NodeID(l.node.LeaderID()), policy)
}

func adapterLeadershipAllowed(
	localNodeID domain.NodeID,
	consensusLeaderID domain.NodeID,
	policy domain.LeaderPolicy,
) bool {
	if localNodeID == "" || localNodeID != consensusLeaderID {
		return false
	}
	if policy.EffectiveMode() == domain.LeaderSelectionAutomatic || policy.NodeID == "" {
		return true
	}
	return policy.NodeID == localNodeID
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
