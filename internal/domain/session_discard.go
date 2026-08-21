package domain

import (
	"fmt"
	"math"
	"time"
)

// DiscardSession records a durable close-without-archive intent. The session
// remains addressable only for runtime reconciliation and is hidden from all
// user session inventories.
func (s *State) DiscardSession(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	at time.Time,
) error {
	if !s.CanPerformSessionAction(actorID, ref, ActionClose) {
		return ErrAccessDenied
	}
	session, err := s.mutableLiveSession(ref)
	if err != nil {
		return err
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	if session.RuntimePhase != RuntimeStarting && session.RuntimePhase != RuntimeIdle &&
		session.RuntimePhase != RuntimeWaitingInput {
		return ErrInvalidState
	}
	if err := discardSession(&session, at); err != nil {
		return err
	}
	s.Sessions[ref.Key()] = session
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	s.repairNavigationAfterClosed(ref)
	return nil
}

func discardSession(session *Session, at time.Time) error {
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.State = SessionDiscarding
	session.DiscardedAt = at
	session.ArchivedAt = time.Time{}
	session.ArchiveID = ""
	session.ArchiveReady = false
	session.ArchiveReason = ""
	session.InteractivePrompt = nil
	session.ResumePending = false
	session.LastEventAt = at
	session.Revision++
	return nil
}

// CompleteSessionDiscard removes the hidden lifecycle record after the origin
// node has proved that the runtime no longer exists.
func (s *State) CompleteSessionDiscard(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if session.State != SessionDiscarding {
		return ErrInvalidState
	}
	if session.OwnerID != actorID && !s.isCurrentOwner(actorID) {
		return ErrAccessDenied
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}

	delete(s.Sessions, ref.Key())
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	for key, grant := range s.Grants {
		if grant.Session == ref {
			delete(s.Grants, key)
		}
	}
	for userID, activity := range s.Navigation.SessionActivityByUser {
		delete(activity, ref.Key())
		if len(activity) == 0 {
			delete(s.Navigation.SessionActivityByUser, userID)
		}
	}
	for userID, views := range s.TelegramSessionViews {
		delete(views, ref.Key())
		if len(views) == 0 {
			delete(s.TelegramSessionViews, userID)
		}
	}
	for userID, card := range s.TelegramResponseCards {
		if card.Session != ref {
			continue
		}
		card.Session = SessionRef{}
		card.SessionRevision = 0
		card.SessionEventAt = time.Time{}
		card.RenderedFinalAt = time.Time{}
		s.TelegramResponseCards[userID] = card
	}
	s.repairNavigationAfterClosed(ref)
	return nil
}
