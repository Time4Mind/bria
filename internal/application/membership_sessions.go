package application

import (
	"sort"

	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Service) LiveSessionsOnNode(
	actor Principal,
	nodeID domain.NodeID,
) ([]domain.Session, error) {
	if !s.IsOwner(actor) {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if _, ok := state.Nodes[nodeID]; !ok {
		return nil, domain.ErrNotFound
	}
	result := make([]domain.Session, 0)
	for _, session := range state.Sessions {
		if session.NodeID == nodeID && session.IsLive() {
			result = append(result, session)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}
