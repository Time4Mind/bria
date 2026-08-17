package application

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Service) ActiveSession(actor Principal) (domain.Session, error) {
	if actor.UserID <= 0 {
		return domain.Session{}, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if state == nil {
		return domain.Session{}, errors.New("state reader returned nil")
	}
	nodeID := state.Navigation.ActiveNodeByUser[actor.UserID]
	sessionID := state.Navigation.ActiveSessionByUserNode[actor.UserID][nodeID]
	ref := domain.SessionRef{NodeID: nodeID, SessionID: sessionID}
	if !state.CanViewSession(actor.UserID, ref) {
		return domain.Session{}, domain.ErrNotFound
	}
	session, ok := state.Sessions[ref.Key()]
	if !ok || !session.IsLive() {
		return domain.Session{}, domain.ErrNotFound
	}
	return session, nil
}

func (s *Service) Session(actor Principal, ref domain.SessionRef) (domain.Session, error) {
	if actor.UserID <= 0 {
		return domain.Session{}, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if state == nil || !state.CanViewSession(actor.UserID, ref) {
		return domain.Session{}, domain.ErrNotFound
	}
	session, ok := state.Sessions[ref.Key()]
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	return session, nil
}

func (s *Service) ShouldQueueInput(actor Principal, ref domain.SessionRef) (bool, error) {
	state := s.reader.State()
	if state == nil || !state.CanPerformSessionAction(actor.UserID, ref, domain.ActionSendInput) {
		return false, domain.ErrNotFound
	}
	session, ok := state.Sessions[ref.Key()]
	node, nodeOK := state.Nodes[ref.NodeID]
	if !ok || !session.IsLive() || !nodeOK || !node.Enabled() {
		return false, domain.ErrInvalidState
	}
	return node.Status != domain.NodeOnline || len(state.DeferredInputs[ref.Key()]) > 0, nil
}

func (s *Service) PublishSessionRuntime(
	ctx context.Context,
	session domain.Session,
	phase domain.RuntimePhase,
	result *domain.SessionOperationResult,
) error {
	return s.apply(ctx, clusterstate.CommandPublishSessionRuntime, clusterstate.PublishSessionRuntime{
		Session: session.Ref(), Generation: session.RuntimeGeneration, Phase: phase, Result: result,
	})
}

func (s *Service) RecordSessionActivity(
	ctx context.Context,
	actor Principal,
	ref domain.SessionRef,
) error {
	return s.apply(ctx, clusterstate.CommandRecordSessionActivity, clusterstate.RecordSessionActivity{
		ActorID: actor.UserID, Session: ref,
	})
}

func (s *Service) QueueDeferredInput(
	ctx context.Context,
	input domain.DeferredSessionInput,
) error {
	return s.apply(ctx, clusterstate.CommandQueueDeferredInput, clusterstate.QueueDeferredInput{Input: input})
}

func (s *Service) ResolveDeferredInput(
	ctx context.Context,
	ref domain.SessionRef,
	operationID string,
	failed bool,
	detail string,
) error {
	return s.apply(ctx, clusterstate.CommandResolveDeferredInput, clusterstate.ResolveDeferredInput{
		Session: ref, OperationID: operationID, Failed: failed, Detail: detail,
	})
}

type DeferredInputHead struct {
	Session domain.Session
	Input   domain.DeferredSessionInput
}

func (s *Service) DeferredInputHeads() []DeferredInputHead {
	state := s.reader.State()
	result := make([]DeferredInputHead, 0, len(state.DeferredInputs))
	for key, queue := range state.DeferredInputs {
		if len(queue) == 0 {
			continue
		}
		session, ok := state.Sessions[key]
		node, nodeOK := state.Nodes[session.NodeID]
		if !ok || !nodeOK || !session.IsLive() || !node.Enabled() ||
			node.Status != domain.NodeOnline || session.ResumePending ||
			session.RuntimeGeneration != queue[0].ExpectedGeneration {
			continue
		}
		result = append(result, DeferredInputHead{Session: session, Input: queue[0]})
	}
	return result
}

func (s *Service) ClearSession(
	ctx context.Context,
	actor Principal,
	session domain.Session,
) error {
	return s.apply(ctx, clusterstate.CommandClearSession, clusterstate.SessionRevision{
		ActorID: actor.UserID, Session: session.Ref(), ExpectedRevision: session.Revision,
	})
}

func (s *Service) RenameSession(
	ctx context.Context,
	actor Principal,
	session domain.Session,
	name string,
) error {
	return s.apply(ctx, clusterstate.CommandRenameSession, clusterstate.RenameSession{
		ActorID: actor.UserID, Session: session.Ref(), ExpectedRevision: session.Revision,
		Name: name,
	})
}

func (s *Service) AvailableSessionName(
	actor Principal,
	ref domain.SessionRef,
	requested string,
) (string, error) {
	state := s.reader.State()
	session, ok := state.Sessions[ref.Key()]
	if !ok {
		return "", domain.ErrNotFound
	}
	if session.OwnerID != actor.UserID || !state.CanPerformSessionAction(actor.UserID, ref, domain.ActionRename) {
		return "", domain.ErrAccessDenied
	}
	return state.AvailableSessionName(session.OwnerID, ref, requested)
}

func (s *Service) SessionsNeedingGeneratedNames() []domain.Session {
	state := s.reader.State()
	result := make([]domain.Session, 0)
	for _, session := range state.Sessions {
		if session.IsLive() && session.NameFormatVersion < domain.SessionNameFormatVersion {
			result = append(result, session)
		}
	}
	domain.SortLive(result)
	return result
}

func (s *Service) CloseSession(
	ctx context.Context,
	actor Principal,
	session domain.Session,
	archiveCommitID string,
) error {
	return s.apply(ctx, clusterstate.CommandCloseSession, clusterstate.SessionRevision{
		ActorID: actor.UserID, Session: session.Ref(), ExpectedRevision: session.Revision,
		ArchiveCommitID: archiveCommitID,
	})
}

func (s *Service) CompleteSessionArchive(
	ctx context.Context,
	actor Principal,
	session domain.Session,
) error {
	return s.apply(ctx, clusterstate.CommandCompleteSessionArchive, clusterstate.SessionRevision{
		ActorID: actor.UserID, Session: session.Ref(), ExpectedRevision: session.Revision,
		ArchiveCommitID: session.ArchiveID,
	})
}

func (s *Service) RestoreSession(
	ctx context.Context,
	actor Principal,
	session domain.Session,
) error {
	return s.apply(ctx, clusterstate.CommandRestoreSession, clusterstate.SessionRevision{
		ActorID: actor.UserID, Session: session.Ref(), ExpectedRevision: session.Revision,
	})
}

func (s *Service) ArchiveFailedStart(ctx context.Context, session domain.Session) error {
	return s.apply(ctx, clusterstate.CommandArchiveSession, clusterstate.ArchiveSession{
		Session: session.Ref(), ExpectedRevision: session.Revision,
		Reason: domain.ArchiveResumeFailed,
	})
}
