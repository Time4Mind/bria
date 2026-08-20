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
	if access, ok := s.Users[userID]; ok && len(s.Users) == 1 && access.Role == RoleOwner {
		// Startup reconciles the bootstrap owner's node access after membership
		// changes. That is not an ownership migration: transport checkpoints and
		// navigation still belong to the same private actor and must survive it.
		allowed := make(map[NodeID]bool, len(s.Nodes))
		for nodeID := range s.Nodes {
			allowed[nodeID] = true
		}
		access.AllowedNodes = allowed
		s.Users[userID] = access
		s.Grants = make(map[string]SessionGrant)
		s.ensureActiveSession(userID)
		return nil
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
	views := s.TelegramSessionViews[previous]
	if views == nil {
		views = s.TelegramSessionViews[userID]
	}
	s.TelegramSessionViews = make(map[UserID]map[string]TelegramSessionView)
	if views != nil {
		s.TelegramSessionViews[userID] = views
	}
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
