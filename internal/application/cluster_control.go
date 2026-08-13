package application

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Service) SetLeadership(leaders interface{ LeaderID() string }) {
	s.leaders = leaders
}

func (s *Service) RequestQuotaRefresh(ctx context.Context, actor Principal) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandRequestQuotaRefresh, struct{}{})
}

func (s *Service) SetTemporaryLeader(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandSetTemporaryLeader, clusterstate.SetTemporaryLeader{
		NodeID: nodeID, Until: s.now().Add(30 * time.Minute),
	})
}

func (s *Service) IsOwner(actor Principal) bool {
	if actor.UserID <= 0 {
		return false
	}
	state := s.reader.State()
	if state == nil {
		return false
	}
	access, ok := state.Users[actor.UserID]
	return ok && access.Role == domain.RoleOwner
}
