package domain

import (
	"fmt"
	"math"
	"time"
)

// ReattachSessionRuntime repairs a false resume-failed archive after the
// origin node proves that the exact deterministic runtime still exists.
func (s *State) ReattachSessionRuntime(
	ref SessionRef,
	expectedGeneration uint64,
	expectedRevision uint64,
	at time.Time,
) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	node, nodeOK := s.Nodes[ref.NodeID]
	if !nodeOK || node.Status != NodeOnline ||
		session.State != SessionArchived || session.ArchiveReason != ArchiveResumeFailed ||
		session.ArchiveID != "" || session.ArchiveReady {
		return ErrInvalidState
	}
	if expectedGeneration == 0 || session.RuntimeGeneration != expectedGeneration ||
		expectedRevision == 0 || session.Revision != expectedRevision {
		return ErrStaleOperation
	}
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.State = SessionLive
	session.RuntimePhase = RuntimeIdle
	session.RuntimeIssue = ""
	session.InteractivePrompt = nil
	session.ResumePending = false
	session.ArchiveDescription = nil
	session.DescriptionVersion = 0
	session.ArchivedAt = time.Time{}
	session.ArchiveReason = ""
	session.ArchiveReady = false
	session.RestoredAt = at
	session.LastEventAt = at
	session.Revision++
	s.Sessions[ref.Key()] = session
	s.ensureActiveSession(session.OwnerID)
	return nil
}
