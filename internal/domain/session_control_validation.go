package domain

import (
	"fmt"
	"strings"
)

func (s *State) IsSessionLost(ref SessionRef) bool {
	session, ok := s.Sessions[ref.Key()]
	if !ok || !session.IsLive() {
		return false
	}
	node, ok := s.Nodes[session.NodeID]
	return ok && node.Status != NodeOnline
}

func (s *State) mutableLiveSession(ref SessionRef) (Session, error) {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return Session{}, ErrNotFound
	}
	if !session.IsLive() {
		return Session{}, ErrInvalidState
	}
	node, ok := s.Nodes[session.NodeID]
	if !ok || node.Status != NodeOnline {
		return Session{}, ErrInvalidState
	}
	return session, nil
}

func requireRevision(session Session, expected uint64) error {
	if expected == 0 || expected != session.Revision {
		return ErrStaleOperation
	}
	return nil
}

func validateOperationResult(result SessionOperationResult) error {
	if strings.TrimSpace(result.OperationID) == "" || len(result.OperationID) > 128 {
		return fmt.Errorf("%w: result operation id must contain 1 to 128 characters", ErrInvalidState)
	}
	if result.Action == "" {
		return fmt.Errorf("%w: result action is required", ErrInvalidState)
	}
	if result.Status != OperationQueued && result.Status != OperationSucceeded && result.Status != OperationFailed {
		return fmt.Errorf("%w: unsupported operation status %q", ErrInvalidState, result.Status)
	}
	if len(result.Detail) > 512 {
		return fmt.Errorf("%w: operation detail is too long", ErrInvalidState)
	}
	return nil
}

func validArchiveReason(reason ArchiveReason) bool {
	switch reason {
	case ArchiveManual, ArchiveIdle, ArchiveNodeReboot, ArchiveResumeFailed:
		return true
	default:
		return false
	}
}
