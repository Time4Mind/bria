package domain

import "time"

type BackgroundNoticeKind string

const (
	BackgroundWorking     BackgroundNoticeKind = "working"
	BackgroundFinished    BackgroundNoticeKind = "finished"
	BackgroundError       BackgroundNoticeKind = "error"
	BackgroundNeedsAction BackgroundNoticeKind = "needs_action"
)

type BackgroundNotice struct {
	Session          SessionRef           `json:"session"`
	Kind             BackgroundNoticeKind `json:"kind"`
	EventRevision    uint64               `json:"event_revision"`
	Acknowledgements int                  `json:"acknowledgements,omitempty"`
	Dismissed        bool                 `json:"dismissed,omitempty"`
	ChangedAt        time.Time            `json:"changed_at"`
	Notified         bool                 `json:"notified,omitempty"`
}

func (s *State) publishBackgroundNotice(
	session Session,
	kind BackgroundNoticeKind,
	at time.Time,
) {
	if !validBackgroundNoticeKind(kind) {
		return
	}
	for userID := range s.Users {
		if !s.CanViewSession(userID, session.Ref()) {
			s.deleteBackgroundNotice(userID, session.Ref())
			continue
		}
		if s.activeSessionRef(userID) == session.Ref() {
			s.deleteBackgroundNotice(userID, session.Ref())
			continue
		}
		s.setBackgroundNotice(userID, session, kind, at)
	}
}

func (s *State) setBackgroundNotice(
	userID UserID,
	session Session,
	kind BackgroundNoticeKind,
	at time.Time,
) {
	s.ensureBackgroundNotices(userID)
	key := session.Ref().Key()
	current, exists := s.Navigation.BackgroundByUser[userID][key]
	if exists && current.Kind == kind && current.EventRevision == session.Revision {
		return
	}
	s.Navigation.BackgroundByUser[userID][key] = BackgroundNotice{
		Session: session.Ref(), Kind: kind, EventRevision: session.Revision,
		ChangedAt: at, Notified: kind == BackgroundWorking,
	}
}

func (s *State) noteSessionBecameBackground(userID UserID, session Session, at time.Time) {
	kind, ok := currentBackgroundKind(session)
	if !ok || !s.CanViewSession(userID, session.Ref()) {
		return
	}
	if current, exists := s.Navigation.BackgroundByUser[userID][session.Ref().Key()]; kind == BackgroundWorking && exists && current.Dismissed {
		s.deleteBackgroundNotice(userID, session.Ref())
	}
	s.setBackgroundNotice(userID, session, kind, at)
}

func (s *State) acknowledgeBackgroundNotice(userID UserID, ref SessionRef) {
	notices := s.Navigation.BackgroundByUser[userID]
	notice, ok := notices[ref.Key()]
	if !ok {
		return
	}
	notice.Acknowledgements++
	preferences, ok := s.Preferences[userID]
	if !ok {
		preferences = DefaultUserPreferences()
	}
	if notice.Acknowledgements >= preferences.EffectiveBackgroundDismissSwitches() {
		notice.Dismissed = true
	}
	notices[ref.Key()] = notice
}

func (s *State) MarkBackgroundNotified(
	userID UserID,
	ref SessionRef,
	eventRevision uint64,
) error {
	if _, ok := s.Users[userID]; !ok {
		return ErrNotFound
	}
	notice, ok := s.Navigation.BackgroundByUser[userID][ref.Key()]
	if !ok || notice.EventRevision != eventRevision {
		return ErrStaleOperation
	}
	notice.Notified = true
	s.Navigation.BackgroundByUser[userID][ref.Key()] = notice
	return nil
}

func (s *State) clearBackgroundSession(ref SessionRef) {
	for userID := range s.Navigation.BackgroundByUser {
		s.deleteBackgroundNotice(userID, ref)
	}
}

func (s *State) clearBackgroundNode(nodeID NodeID) {
	for userID, notices := range s.Navigation.BackgroundByUser {
		for key, notice := range notices {
			if notice.Session.NodeID == nodeID {
				delete(notices, key)
			}
		}
		if len(notices) == 0 {
			delete(s.Navigation.BackgroundByUser, userID)
		}
	}
}

func (s *State) deleteBackgroundNotice(userID UserID, ref SessionRef) {
	notices := s.Navigation.BackgroundByUser[userID]
	delete(notices, ref.Key())
	if len(notices) == 0 {
		delete(s.Navigation.BackgroundByUser, userID)
	}
}

func (s *State) ensureBackgroundNotices(userID UserID) {
	if s.Navigation.BackgroundByUser == nil {
		s.Navigation.BackgroundByUser = make(map[UserID]map[string]BackgroundNotice)
	}
	if s.Navigation.BackgroundByUser[userID] == nil {
		s.Navigation.BackgroundByUser[userID] = make(map[string]BackgroundNotice)
	}
}

func (s *State) cloneBackgroundNotices(target *State) {
	for userID, notices := range s.Navigation.BackgroundByUser {
		copyNotices := make(map[string]BackgroundNotice, len(notices))
		for key, notice := range notices {
			copyNotices[key] = notice
		}
		target.Navigation.BackgroundByUser[userID] = copyNotices
	}
}

func (s *State) activeSessionRef(userID UserID) SessionRef {
	nodeID := s.Navigation.ActiveNodeByUser[userID]
	return SessionRef{NodeID: nodeID, SessionID: s.Navigation.ActiveSessionByUserNode[userID][nodeID]}
}

func currentBackgroundKind(session Session) (BackgroundNoticeKind, bool) {
	switch session.RuntimePhase {
	case RuntimeStarting, RuntimeRunning, RuntimeStopping:
		return BackgroundWorking, true
	case RuntimeWaitingInput:
		return BackgroundNeedsAction, true
	case RuntimeDegraded:
		if session.LastOperation != nil && session.LastOperation.Status == OperationFailed {
			return BackgroundError, true
		}
	}
	return "", false
}

func (s *State) publishBackgroundTransition(
	previous RuntimePhase,
	session Session,
	result *SessionOperationResult,
	at time.Time,
) {
	if result != nil && result.Status == OperationFailed {
		s.publishBackgroundNotice(session, BackgroundError, at)
		return
	}
	switch session.RuntimePhase {
	case RuntimeWaitingInput:
		s.publishBackgroundNotice(session, BackgroundNeedsAction, at)
	case RuntimeRunning, RuntimeStarting:
		if previous != session.RuntimePhase || result != nil {
			s.publishBackgroundNotice(session, BackgroundWorking, at)
		}
	case RuntimeIdle:
		if previous == RuntimeRunning && result == nil {
			s.publishBackgroundNotice(session, BackgroundFinished, at)
		} else if result != nil && result.Action != ActionSendInput {
			s.clearBackgroundSession(session.Ref())
		}
	}
}

func validBackgroundNoticeKind(kind BackgroundNoticeKind) bool {
	switch kind {
	case BackgroundWorking, BackgroundFinished, BackgroundError, BackgroundNeedsAction:
		return true
	default:
		return false
	}
}
