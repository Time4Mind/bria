package domain

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

type BootRecoveryPlan struct {
	Recover   []SessionRef `json:"recover"`
	Archived  []SessionRef `json:"archived"`
	Discarded []SessionRef `json:"discarded,omitempty"`
}

func (s *State) ObserveNodeBoot(nodeID NodeID, bootID string, at time.Time) (BootRecoveryPlan, error) {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return BootRecoveryPlan{}, ErrNotFound
	}
	if strings.TrimSpace(bootID) == "" {
		return BootRecoveryPlan{}, fmt.Errorf("boot id is required")
	}
	previous := node.BootID
	node.BootID = bootID
	node.LastSeenAt = at
	s.Nodes[nodeID] = node
	if previous == "" {
		return BootRecoveryPlan{}, nil
	}
	if SameBootIdentity(previous, bootID) {
		plan := BootRecoveryPlan{}
		for _, session := range s.Sessions {
			if session.NodeID == nodeID && session.IsLive() && session.ResumePending {
				plan.Recover = append(plan.Recover, session.Ref())
			}
		}
		sortSessionRefs(plan.Recover)
		return plan, nil
	}

	plan := BootRecoveryPlan{}
	for key, session := range s.Sessions {
		if session.NodeID != nodeID || !session.IsLive() {
			continue
		}
		if session.UserRequestTracked && !session.UserRequestSeen {
			if err := discardSession(&session, at); err != nil {
				return BootRecoveryPlan{}, err
			}
			plan.Discarded = append(plan.Discarded, session.Ref())
			s.Sessions[key] = session
			continue
		}
		if session.RuntimePhase == RuntimeStarting {
			// A committed creation intent is replayable even if the host rebooted
			// before the provider process appeared. The leader reconciler will
			// idempotently create the deterministic tmux window.
			s.Sessions[key] = session
			continue
		}
		if s.sessionIdleDeadlinePassed(session, at) {
			if err := archiveSession(&session, at, ArchiveIdle); err != nil {
				return BootRecoveryPlan{}, err
			}
			plan.Archived = append(plan.Archived, session.Ref())
		} else if session.ProviderSessionID == "" {
			if err := archiveSession(&session, at, ArchiveResumeFailed); err != nil {
				return BootRecoveryPlan{}, err
			}
			plan.Archived = append(plan.Archived, session.Ref())
		} else {
			if session.Revision == math.MaxUint64 {
				return BootRecoveryPlan{}, fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
			}
			session.RuntimePhase = RuntimeDegraded
			session.InteractivePrompt = nil
			session.ResumePending = true
			session.Revision++
			plan.Recover = append(plan.Recover, session.Ref())
		}
		s.Sessions[key] = session
	}
	sortSessionRefs(plan.Recover)
	sortSessionRefs(plan.Archived)
	sortSessionRefs(plan.Discarded)
	for _, ref := range plan.Archived {
		s.clearDeferredInputs(ref)
		s.clearBackgroundSession(ref)
		s.repairNavigationAfterUnavailable(ref)
	}
	for _, ref := range plan.Discarded {
		s.clearDeferredInputs(ref)
		s.clearBackgroundSession(ref)
		s.repairNavigationAfterUnavailable(ref)
	}
	return plan, nil
}

// SameBootIdentity accepts the stable Darwin format and the historical format
// that included kern.boottime microseconds. macOS may change that fractional
// component without rebooting, so only the boot second identifies the boot.
func SameBootIdentity(previous, current string) bool {
	if previous == current {
		return true
	}
	previousSecond, previousDarwin := darwinBootSecond(previous)
	currentSecond, currentDarwin := darwinBootSecond(current)
	return previousDarwin && currentDarwin && previousSecond == currentSecond
}

func darwinBootSecond(value string) (uint64, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 || parts[0] != "darwin" {
		return 0, false
	}
	seconds, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	if len(parts) == 3 {
		microseconds, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil || microseconds >= 1_000_000 {
			return 0, false
		}
	}
	return seconds, true
}

func sortSessionRefs(refs []SessionRef) {
	slices.SortFunc(refs, func(a, b SessionRef) int {
		if order := cmp.Compare(a.NodeID, b.NodeID); order != 0 {
			return order
		}
		return cmp.Compare(a.SessionID, b.SessionID)
	})
}

func (s *State) sessionIdleDeadlinePassed(session Session, at time.Time) bool {
	preferenceOwner := s.OwnerID()
	if preferenceOwner == 0 {
		preferenceOwner = session.OwnerID
	}
	preferences, ok := s.Preferences[preferenceOwner]
	if !ok {
		preferences = DefaultUserPreferences()
	}
	if preferences.IdleArchiveHours == 0 {
		return false
	}
	lastActivity := session.LastEventAt
	if lastActivity.IsZero() {
		lastActivity = session.CreatedAt
	}
	deadline := lastActivity.Add(time.Duration(preferences.IdleArchiveHours) * time.Hour)
	return !at.Before(deadline)
}

func (s *State) CompleteBootRecovery(ref SessionRef, at time.Time) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if !session.IsLive() || !session.ResumePending {
		return ErrInvalidState
	}
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.RuntimePhase = RuntimeIdle
	session.InteractivePrompt = nil
	session.ResumePending = false
	session.Revision++
	if session.LiveSinceAt.IsZero() {
		session.LiveSinceAt = session.CreatedAt
	}
	session.RestoredAt = at
	session.ArchivedAt = time.Time{}
	session.ArchiveReason = ""
	session.ArchiveReady = false
	s.Sessions[ref.Key()] = session
	return nil
}

func (s *State) FailBootRecovery(ref SessionRef, at time.Time) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if !session.IsLive() || !session.ResumePending {
		return ErrInvalidState
	}
	if err := archiveSession(&session, at, ArchiveResumeFailed); err != nil {
		return err
	}
	s.Sessions[ref.Key()] = session
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	s.repairNavigationAfterUnavailable(ref)
	return nil
}

func (s *State) MarkMissingOnSameBoot(
	ref SessionRef,
	archiveID string,
	expectedGeneration uint64,
	expectedRevision uint64,
	checkVersion bool,
	at time.Time,
) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if !session.IsLive() {
		return ErrInvalidState
	}
	if checkVersion && (session.RuntimeGeneration != expectedGeneration ||
		session.Revision != expectedRevision) {
		return ErrStaleOperation
	}
	if strings.TrimSpace(archiveID) == "" || len(archiveID) > 128 ||
		strings.ContainsAny(archiveID, "/\\") {
		return fmt.Errorf("%w: archive id is invalid", ErrInvalidState)
	}
	if err := archiveSession(&session, at, ArchiveResumeFailed); err != nil {
		return err
	}
	session.ArchiveID = archiveID
	s.Sessions[ref.Key()] = session
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	s.repairNavigationAfterUnavailable(ref)
	return nil
}

func archiveSession(session *Session, at time.Time, reason ArchiveReason) error {
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.State = SessionArchived
	session.RuntimePhase = RuntimeIdle
	session.InteractivePrompt = nil
	session.ResumePending = false
	session.ArchivedAt = at
	session.ArchiveReason = reason
	session.ArchiveReady = false
	session.LastEventAt = at
	session.Revision++
	return nil
}
