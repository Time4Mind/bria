package application

import (
	"cmp"
	"errors"
	"slices"

	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Service) CallbackNodeCandidates(actor Principal) ([]domain.NodeID, error) {
	if actor.UserID <= 0 {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if state == nil {
		return nil, errors.New("state reader returned nil")
	}
	if _, ok := state.Users[actor.UserID]; !ok {
		return nil, domain.ErrAccessDenied
	}
	nodes := state.VisibleNodes(actor.UserID)
	result := make([]domain.NodeID, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.ID)
	}
	slices.SortFunc(result, func(a, b domain.NodeID) int { return cmp.Compare(a, b) })
	return result, nil
}

func (s *Service) CallbackSessionCandidates(actor Principal) ([]domain.SessionRef, error) {
	if actor.UserID <= 0 {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if state == nil {
		return nil, errors.New("state reader returned nil")
	}
	if _, ok := state.Users[actor.UserID]; !ok {
		return nil, domain.ErrAccessDenied
	}
	result := make([]domain.SessionRef, 0)
	for _, session := range state.Sessions {
		if state.CanViewSession(actor.UserID, session.Ref()) {
			result = append(result, session.Ref())
		}
	}
	slices.SortFunc(result, func(a, b domain.SessionRef) int {
		if order := cmp.Compare(a.NodeID, b.NodeID); order != 0 {
			return order
		}
		return cmp.Compare(a.SessionID, b.SessionID)
	})
	return result, nil
}

func (s *Service) CallbackArchiveCandidates(actor Principal) ([]domain.SessionRef, error) {
	refs, err := s.CallbackSessionCandidates(actor)
	if err != nil {
		return nil, err
	}
	state := s.reader.State()
	result := refs[:0]
	for _, ref := range refs {
		if session, ok := state.Sessions[ref.Key()]; ok && session.State == domain.SessionArchived {
			result = append(result, ref)
		}
	}
	return result, nil
}
