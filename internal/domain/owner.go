package domain

import "time"

func (s *State) OwnerID() UserID {
	var owner UserID
	for userID, access := range s.Users {
		if access.Role == RoleOwner && (owner == 0 || userID < owner) {
			owner = userID
		}
	}
	return owner
}

// SetSoleOwner collapses legacy access state into Bria's current single-owner
// model. Sessions stay private and retain their server-local runtime identity.
func (s *State) SetSoleOwner(userID UserID) error {
	if userID <= 0 {
		return ErrInvalidState
	}
	previous := s.OwnerID()
	if access, ok := s.Users[userID]; ok && access.Role == RoleOwner {
		previous = userID
	}
	preferences := DefaultUserPreferences()
	if current, ok := s.Preferences[previous]; ok {
		preferences = current.clone()
	} else if current, ok := s.Preferences[userID]; ok {
		preferences = current.clone()
	}
	allowed := make(map[NodeID]bool, len(s.Nodes))
	for nodeID := range s.Nodes {
		allowed[nodeID] = true
	}
	s.Users = map[UserID]UserAccess{userID: {Role: RoleOwner, AllowedNodes: allowed}}
	s.Preferences = map[UserID]UserPreferences{userID: preferences}
	s.Grants = make(map[string]SessionGrant)
	s.TelegramResponseCards = make(map[UserID]TelegramResponseCard)
	s.moveOwnerNavigation(previous, userID)
	s.ensureActiveSession(userID)
	return nil
}

func (s *State) moveOwnerNavigation(previous, current UserID) {
	activeNode := s.Navigation.ActiveNodeByUser[previous]
	activeSessions := s.Navigation.ActiveSessionByUserNode[previous]
	activity := s.Navigation.SessionActivityByUser[previous]
	background := s.Navigation.BackgroundByUser[previous]
	s.Navigation.ActiveNodeByUser = make(map[UserID]NodeID)
	s.Navigation.ActiveSessionByUserNode = make(map[UserID]map[NodeID]SessionID)
	s.Navigation.SessionActivityByUser = make(map[UserID]map[string]time.Time)
	s.Navigation.BackgroundByUser = make(map[UserID]map[string]BackgroundNotice)
	if activeNode != "" {
		s.Navigation.ActiveNodeByUser[current] = activeNode
	}
	if activeSessions != nil {
		s.Navigation.ActiveSessionByUserNode[current] = activeSessions
	}
	if activity != nil {
		s.Navigation.SessionActivityByUser[current] = activity
	}
	if background != nil {
		s.Navigation.BackgroundByUser[current] = background
	}
}
