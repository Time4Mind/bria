package clusterupdate

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

// Progress combines durable rollout state with the current node-local updater
// stage. Durable pending/installing/healthy transitions remain authoritative;
// detailed download progress is intentionally observational and can be rebuilt
// after a leader change by querying the installing node.
func (c *Coordinator) Progress(
	ctx context.Context, updateID string,
) map[domain.NodeID]Status {
	state := c.reader.State()
	result := make(map[domain.NodeID]Status)
	if state.ClusterUpdate == nil || state.ClusterUpdate.ID != updateID {
		return result
	}
	update := state.ClusterUpdate
	for _, nodeID := range update.Order {
		nodeUpdate := update.Nodes[nodeID]
		status := Status{
			NodeID: string(nodeID), UpdateID: update.ID, Version: update.Version,
			StartedAt: nodeUpdate.StartedAt, UpdatedAt: nodeUpdate.UpdatedAt,
		}
		switch nodeUpdate.Phase {
		case domain.NodeUpdatePending:
			status.Phase = PhaseWaiting
		case domain.NodeUpdateHealthy:
			status.Phase, status.Progress = PhaseHealthy, 100
		case domain.NodeUpdateFailed:
			status.Phase, status.Error = PhaseFailed, nodeUpdate.Error
		case domain.NodeUpdateInstalling:
			node := state.Nodes[nodeID]
			if node.Status == domain.NodeOnline && node.Version == update.Version {
				status.Phase, status.Progress = PhaseVerifying, 98
				break
			}
			requestCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			live, err := c.nodes.Status(requestCtx, Request{
				NodeID: string(nodeID), UpdateID: update.ID,
			})
			cancel()
			if err != nil {
				status.Phase, status.Progress = PhaseRestarting, 95
				break
			}
			status = live
			if status.StartedAt.IsZero() {
				status.StartedAt = nodeUpdate.StartedAt
			}
		}
		result[nodeID] = status
	}
	return result
}
