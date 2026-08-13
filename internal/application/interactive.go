package application

import (
	"cmp"
	"slices"

	"github.com/Time4Mind/bria/internal/domain"
)

type InteractiveNotice struct {
	UserID  domain.UserID
	Session domain.Session
	Node    domain.Node
	Active  bool
}

// InteractiveNotices returns only prompts the user may control. View-only
// shares remain visible in their session card but never receive action alerts.
func (s *Service) InteractiveNotices() []InteractiveNotice {
	state := s.reader.State()
	if state == nil {
		return nil
	}
	result := make([]InteractiveNotice, 0)
	for userID := range state.Users {
		activeNode := state.Navigation.ActiveNodeByUser[userID]
		activeSession := state.Navigation.ActiveSessionByUserNode[userID][activeNode]
		for _, session := range state.Sessions {
			if session.InteractivePrompt == nil || !session.IsLive() ||
				!state.CanControlSession(userID, session.Ref()) {
				continue
			}
			node, ok := state.Nodes[session.NodeID]
			if !ok || node.Status != domain.NodeOnline {
				continue
			}
			result = append(result, InteractiveNotice{
				UserID: userID, Session: session, Node: node,
				Active: session.NodeID == activeNode && session.ID == activeSession,
			})
		}
	}
	slices.SortFunc(result, func(a, b InteractiveNotice) int {
		if order := cmp.Compare(a.UserID, b.UserID); order != 0 {
			return order
		}
		return cmp.Compare(a.Session.Ref().Key(), b.Session.Ref().Key())
	})
	return result
}
