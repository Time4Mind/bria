package domain

import "fmt"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type ShareMode string

const (
	ShareView    ShareMode = "view"
	ShareControl ShareMode = "control"
)

type UserAccess struct {
	Role         Role            `json:"role"`
	AllowedNodes map[NodeID]bool `json:"allowed_nodes"`
}

type SessionGrant struct {
	Session SessionRef `json:"session"`
	UserID  UserID     `json:"user_id"`
	Mode    ShareMode  `json:"mode"`
}

type SessionAction string

const (
	ActionView          SessionAction = "view"
	ActionSendInput     SessionAction = "send_input"
	ActionSendKey       SessionAction = "send_key"
	ActionCapture       SessionAction = "capture"
	ActionStop          SessionAction = "stop"
	ActionClear         SessionAction = "clear"
	ActionClose         SessionAction = "close"
	ActionOpenTerminal  SessionAction = "open_terminal"
	ActionArchive       SessionAction = "archive"
	ActionRestore       SessionAction = "restore"
	ActionDelete        SessionAction = "delete"
	ActionRename        SessionAction = "rename"
	ActionManageSharing SessionAction = "manage_sharing"
)

func (s *State) CanAccessNode(userID UserID, nodeID NodeID) bool {
	access, ok := s.Users[userID]
	return ok && access.AllowedNodes[nodeID]
}

func (s *State) CanViewSession(userID UserID, ref SessionRef) bool {
	session, ok := s.Sessions[ref.Key()]
	if !ok || !s.CanAccessNode(userID, session.NodeID) {
		return false
	}
	if s.isCurrentOwner(userID) || session.OwnerID == userID {
		return true
	}
	_, ok = s.Grants[grantKey(ref, userID)]
	return ok
}

func (s *State) CanControlSession(userID UserID, ref SessionRef) bool {
	session, ok := s.Sessions[ref.Key()]
	if !ok || !s.CanAccessNode(userID, session.NodeID) {
		return false
	}
	if s.isCurrentOwner(userID) || session.OwnerID == userID {
		return true
	}
	grant, ok := s.Grants[grantKey(ref, userID)]
	return ok && grant.Mode == ShareControl
}

func (s *State) CanPerformSessionAction(
	userID UserID,
	ref SessionRef,
	action SessionAction,
) bool {
	session, ok := s.Sessions[ref.Key()]
	if !ok || !s.CanAccessNode(userID, session.NodeID) {
		return false
	}
	if s.isCurrentOwner(userID) || session.OwnerID == userID {
		return true
	}
	grant, ok := s.Grants[grantKey(ref, userID)]
	if !ok {
		return false
	}
	switch grant.Mode {
	case ShareView:
		return action == ActionView || action == ActionCapture
	case ShareControl:
		return action == ActionView || action == ActionCapture ||
			action == ActionSendInput || action == ActionSendKey ||
			action == ActionStop
	default:
		return false
	}
}

func (s *State) ShareSession(
	actorID UserID,
	ref SessionRef,
	recipientID UserID,
	mode ShareMode,
) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if session.OwnerID != actorID && !s.isCurrentOwner(actorID) {
		return ErrAccessDenied
	}
	if mode != ShareView && mode != ShareControl {
		return fmt.Errorf("%w: unsupported share mode", ErrInvalidState)
	}
	if !s.CanAccessNode(recipientID, session.NodeID) {
		return fmt.Errorf("%w: recipient cannot access node", ErrAccessDenied)
	}
	s.Grants[grantKey(ref, recipientID)] = SessionGrant{
		Session: ref,
		UserID:  recipientID,
		Mode:    mode,
	}
	s.ensureActiveSession(recipientID)
	return nil
}

func (s *State) RevokeSessionShare(actorID UserID, ref SessionRef, userID UserID) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if session.OwnerID != actorID && !s.isCurrentOwner(actorID) {
		return ErrAccessDenied
	}
	delete(s.Grants, grantKey(ref, userID))
	s.deleteBackgroundNotice(userID, ref)
	s.ensureActiveSession(userID)
	return nil
}

func (s *State) isCurrentOwner(userID UserID) bool {
	access, ok := s.Users[userID]
	return ok && access.Role == RoleOwner
}

func grantKey(ref SessionRef, userID UserID) string {
	return fmt.Sprintf("%s:%d", ref.Key(), userID)
}
