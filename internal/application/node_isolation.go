package application

import (
	"context"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Service) SetNodeBackendIsolationRequired(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
	required bool,
) error {
	if !s.IsAdmin(actor) {
		return domain.ErrAccessDenied
	}
	state := s.reader.State()
	node, ok := state.Nodes[nodeID]
	if !ok || !state.CanAccessNode(actor.UserID, nodeID) {
		return domain.ErrNotFound
	}
	if required && !node.BackendIsolation.Ready {
		for _, session := range state.Sessions {
			if session.NodeID == nodeID && session.IsLive() {
				return domain.ErrInvalidState
			}
		}
	}
	return s.apply(ctx, clusterstate.CommandSetNodeIsolation, clusterstate.SetNodeIsolation{
		NodeID: nodeID, Required: required,
	})
}
