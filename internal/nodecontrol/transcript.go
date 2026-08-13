package nodecontrol

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type TranscriptQuery struct {
	ActorID            int64  `json:"actor_id"`
	NodeID             string `json:"node_id"`
	SessionID          string `json:"session_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

type TranscriptReader interface {
	ReadTranscript(context.Context, TranscriptQuery) ([]transcript.Event, error)
}

type TranscriptSource interface {
	Read(context.Context, transcript.Request) ([]transcript.Event, error)
}

type ArchivedTranscriptSource interface {
	ReadArchivedTranscript(context.Context, domain.Session) ([]transcript.Event, error)
}

// LocalTranscriptService resolves transcript metadata from replicated state.
// Neither the caller nor the wire protocol can supply a filesystem path.
type LocalTranscriptService struct {
	nodeID   string
	state    StateReader
	source   TranscriptSource
	archives ArchivedTranscriptSource
}

func NewLocalTranscriptService(
	nodeID string,
	state StateReader,
	source TranscriptSource,
	archives ...ArchivedTranscriptSource,
) (*LocalTranscriptService, error) {
	if nodeID == "" || state == nil || source == nil {
		return nil, errors.New("node id, state reader, and transcript source are required")
	}
	service := &LocalTranscriptService{nodeID: nodeID, state: state, source: source}
	if len(archives) > 1 {
		return nil, errors.New("at most one archived transcript source is supported")
	}
	if len(archives) == 1 {
		if archives[0] == nil {
			return nil, errors.New("archived transcript source is nil")
		}
		service.archives = archives[0]
	}
	return service, nil
}

func (s *LocalTranscriptService) ReadTranscript(
	ctx context.Context,
	query TranscriptQuery,
) ([]transcript.Event, error) {
	if query.ActorID <= 0 || query.NodeID != s.nodeID || query.SessionID == "" ||
		query.ExpectedGeneration == 0 {
		return nil, domain.ErrAccessDenied
	}
	state := s.state.State()
	if state == nil {
		return nil, domain.ErrAccessDenied
	}
	ref := domain.SessionRef{
		NodeID: domain.NodeID(query.NodeID), SessionID: domain.SessionID(query.SessionID),
	}
	session, ok := state.Sessions[ref.Key()]
	if !ok || session.RuntimeGeneration != query.ExpectedGeneration ||
		!state.CanViewSession(domain.UserID(query.ActorID), ref) {
		return nil, domain.ErrNotFound
	}
	if session.State == domain.SessionArchived && session.ArchiveReady &&
		session.ArchiveID != "" && s.archives != nil {
		return s.archives.ReadArchivedTranscript(ctx, session)
	}
	if session.ProviderSessionID == "" {
		return nil, nil
	}
	return s.source.Read(ctx, transcript.Request{
		Backend: transcript.Backend(session.Backend), ProviderSessionID: session.ProviderSessionID,
		Workdir: session.Workdir,
	})
}

type TranscriptRouter struct {
	localNodeID string
	local       TranscriptReader
	remote      TranscriptReader
}

func NewTranscriptRouter(
	localNodeID string,
	local TranscriptReader,
	remote TranscriptReader,
) (*TranscriptRouter, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("local node id and transcript readers are required")
	}
	return &TranscriptRouter{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *TranscriptRouter) ReadTranscript(
	ctx context.Context,
	query TranscriptQuery,
) ([]transcript.Event, error) {
	if query.NodeID == r.localNodeID {
		return r.local.ReadTranscript(ctx, query)
	}
	return r.remote.ReadTranscript(ctx, query)
}
