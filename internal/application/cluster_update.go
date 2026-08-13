package application

import (
	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Service) ClusterUpdate(
	actor Principal,
) (*domain.ClusterUpdate, map[domain.NodeID]domain.Node, error) {
	if !s.IsOwner(actor) {
		return nil, nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	nodes := make(map[domain.NodeID]domain.Node, len(state.Nodes))
	for id, node := range state.Nodes {
		nodes[id] = node
	}
	if state.ClusterUpdate == nil {
		return nil, nodes, nil
	}
	update := *state.ClusterUpdate
	update.Order = append([]domain.NodeID(nil), update.Order...)
	update.Nodes = make(map[domain.NodeID]domain.NodeUpdate, len(state.ClusterUpdate.Nodes))
	for id, node := range state.ClusterUpdate.Nodes {
		update.Nodes[id] = node
	}
	return &update, nodes, nil
}
