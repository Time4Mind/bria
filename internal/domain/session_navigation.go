package domain

import "time"

func (s *State) SelectNode(userID UserID, nodeID NodeID, at time.Time) error {
	if !s.CanAccessNode(userID, nodeID) {
		return ErrNotFound
	}
	if s.Navigation.ActiveNodeByUser == nil {
		s.Navigation.ActiveNodeByUser = make(map[UserID]NodeID)
	}
	if s.Navigation.ActiveSessionByUserNode == nil {
		s.Navigation.ActiveSessionByUserNode = make(map[UserID]map[NodeID]SessionID)
	}
	if s.Navigation.ActiveSessionByUserNode[userID] == nil {
		s.Navigation.ActiveSessionByUserNode[userID] = make(map[NodeID]SessionID)
	}
	selected := SessionRef{
		NodeID: nodeID, SessionID: s.Navigation.ActiveSessionByUserNode[userID][nodeID],
	}
	if !s.sessionAvailableToUser(userID, selected) {
		if replacement, ok := s.mostRecentAvailableSession(userID, nodeID); ok {
			s.Navigation.ActiveSessionByUserNode[userID][nodeID] = replacement.SessionID
		}
	}
	if previous := s.activeSessionRef(userID); previous.SessionID != "" && previous.NodeID != nodeID {
		if session, ok := s.Sessions[previous.Key()]; ok {
			s.noteSessionBecameBackground(userID, session, at)
		}
		target := SessionRef{
			NodeID:    nodeID,
			SessionID: s.Navigation.ActiveSessionByUserNode[userID][nodeID],
		}
		if target.SessionID != "" {
			s.acknowledgeBackgroundNotice(userID, target)
		}
	}
	s.Navigation.ActiveNodeByUser[userID] = nodeID
	return nil
}

func (s *State) SelectSession(userID UserID, ref SessionRef, at time.Time) error {
	if !s.CanViewSession(userID, ref) {
		return ErrNotFound
	}
	session := s.Sessions[ref.Key()]
	if !session.IsLive() {
		return ErrInvalidState
	}
	if s.Navigation.ActiveNodeByUser == nil {
		s.Navigation.ActiveNodeByUser = make(map[UserID]NodeID)
	}
	if s.Navigation.ActiveSessionByUserNode == nil {
		s.Navigation.ActiveSessionByUserNode = make(map[UserID]map[NodeID]SessionID)
	}
	if _, ok := s.Navigation.ActiveSessionByUserNode[userID]; !ok {
		s.Navigation.ActiveSessionByUserNode[userID] = make(map[NodeID]SessionID)
	}
	if s.Navigation.SessionActivityByUser == nil {
		s.Navigation.SessionActivityByUser = make(map[UserID]map[string]time.Time)
	}
	if _, ok := s.Navigation.SessionActivityByUser[userID]; !ok {
		s.Navigation.SessionActivityByUser[userID] = make(map[string]time.Time)
	}
	previous := s.activeSessionRef(userID)
	if previous == ref {
		return nil
	}
	if previous.SessionID != "" {
		if previousSession, ok := s.Sessions[previous.Key()]; ok {
			s.noteSessionBecameBackground(userID, previousSession, at)
		}
	}
	s.acknowledgeBackgroundNotice(userID, ref)
	s.Navigation.ActiveNodeByUser[userID] = ref.NodeID
	s.Navigation.ActiveSessionByUserNode[userID][ref.NodeID] = ref.SessionID
	s.Navigation.SessionActivityByUser[userID][ref.Key()] = at
	return nil
}

// RecordSessionActivity records actor-specific recency used when an active
// session closes. It is replicated because a different leader must make the
// same fallback choice.
func (s *State) RecordSessionActivity(userID UserID, ref SessionRef, at time.Time) error {
	if !s.CanPerformSessionAction(userID, ref, ActionSendInput) {
		return ErrAccessDenied
	}
	session := s.Sessions[ref.Key()]
	node, ok := s.Nodes[session.NodeID]
	if !ok || node.Status != NodeOnline || !session.IsLive() {
		return ErrInvalidState
	}
	if s.Navigation.SessionActivityByUser == nil {
		s.Navigation.SessionActivityByUser = make(map[UserID]map[string]time.Time)
	}
	if s.Navigation.SessionActivityByUser[userID] == nil {
		s.Navigation.SessionActivityByUser[userID] = make(map[string]time.Time)
	}
	s.Navigation.SessionActivityByUser[userID][ref.Key()] = at
	return nil
}

func (s *State) repairNavigationAfterUnavailable(ref SessionRef) {
	for userID := range s.Users {
		perNode := s.Navigation.ActiveSessionByUserNode[userID]
		wasActive := perNode != nil && perNode[ref.NodeID] == ref.SessionID
		if !wasActive && s.Navigation.ActiveNodeByUser[userID] != "" {
			continue
		}
		delete(perNode, ref.NodeID)
		s.ensureActiveSession(userID)
	}
}

func (s *State) ensureActiveSession(userID UserID) {
	if s.Navigation.ActiveNodeByUser == nil {
		s.Navigation.ActiveNodeByUser = make(map[UserID]NodeID)
	}
	if s.Navigation.ActiveSessionByUserNode == nil {
		s.Navigation.ActiveSessionByUserNode = make(map[UserID]map[NodeID]SessionID)
	}
	if s.Navigation.ActiveSessionByUserNode[userID] == nil {
		s.Navigation.ActiveSessionByUserNode[userID] = make(map[NodeID]SessionID)
	}

	activeNode := s.Navigation.ActiveNodeByUser[userID]
	activeSession := s.Navigation.ActiveSessionByUserNode[userID][activeNode]
	if activeSession != "" && s.sessionAvailableToUser(userID, SessionRef{
		NodeID: activeNode, SessionID: activeSession,
	}) {
		return
	}

	if replacement, ok := s.mostRecentAvailableSession(userID, activeNode); ok {
		s.Navigation.ActiveSessionByUserNode[userID][replacement.NodeID] = replacement.SessionID
		s.Navigation.ActiveNodeByUser[userID] = replacement.NodeID
		return
	}
	if replacement, ok := s.mostRecentAvailableSession(userID, ""); ok {
		s.Navigation.ActiveSessionByUserNode[userID][replacement.NodeID] = replacement.SessionID
		s.Navigation.ActiveNodeByUser[userID] = replacement.NodeID
		return
	}
	delete(s.Navigation.ActiveNodeByUser, userID)
}

func (s *State) mostRecentAvailableSession(userID UserID, nodeID NodeID) (SessionRef, bool) {
	var selected Session
	found := false
	for _, candidate := range s.Sessions {
		if nodeID != "" && candidate.NodeID != nodeID {
			continue
		}
		if !s.sessionAvailableToUser(userID, candidate.Ref()) {
			continue
		}
		if !found || s.sessionMoreRecentForUser(userID, candidate, selected) {
			selected = candidate
			found = true
		}
	}
	return selected.Ref(), found
}

func (s *State) sessionAvailableToUser(userID UserID, ref SessionRef) bool {
	session, ok := s.Sessions[ref.Key()]
	if !ok || !session.IsLive() || !s.CanViewSession(userID, ref) {
		return false
	}
	node, ok := s.Nodes[session.NodeID]
	return ok && node.Status == NodeOnline
}

func (s *State) sessionMoreRecentForUser(userID UserID, left, right Session) bool {
	activity := s.Navigation.SessionActivityByUser[userID]
	leftActivity := activity[left.Ref().Key()]
	rightActivity := activity[right.Ref().Key()]
	if order := leftActivity.Compare(rightActivity); order != 0 {
		return order > 0
	}
	leftStarted := left.LiveSinceAt
	if leftStarted.IsZero() {
		leftStarted = left.CreatedAt
	}
	rightStarted := right.LiveSinceAt
	if rightStarted.IsZero() {
		rightStarted = right.CreatedAt
	}
	if order := leftStarted.Compare(rightStarted); order != 0 {
		return order > 0
	}
	return left.Ref().Key() > right.Ref().Key()
}
