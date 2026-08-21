package sessiondescription

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type Request struct {
	NodeID           domain.NodeID     `json:"node_id"`
	Session          domain.SessionRef `json:"session"`
	ArchiveID        string            `json:"archive_id"`
	ExpectedRevision uint64            `json:"expected_revision"`
}

type Result struct {
	Lines []string `json:"lines,omitempty"`
	Empty bool     `json:"empty,omitempty"`
}

type Service interface {
	Generate(context.Context, Request) (Result, error)
}

type StateReader interface{ State() *domain.State }

type PromptSource interface {
	ReadFirstUserTexts(context.Context, transcript.Request, int) ([]string, error)
	Discover(context.Context, transcript.Backend, string, int, int) (transcript.Discovery, error)
	DiscoverFresh(context.Context, transcript.Backend, string, int, int) (transcript.Discovery, error)
}

type ArchiveSource interface {
	ReadArchivedInitialUserPrompts(context.Context, domain.Session) ([]string, error)
	ReadArchivedTranscript(context.Context, domain.Session) ([]transcript.Event, error)
}

type DescriptionGenerator interface {
	Generate(context.Context, string, []string) ([]string, error)
}

type LocalService struct {
	nodeID    domain.NodeID
	state     StateReader
	prompts   PromptSource
	archives  ArchiveSource
	generator DescriptionGenerator
}

func NewLocalService(
	nodeID domain.NodeID,
	state StateReader,
	prompts PromptSource,
	archives ArchiveSource,
	generator DescriptionGenerator,
) (*LocalService, error) {
	if nodeID == "" || state == nil || prompts == nil || archives == nil || generator == nil {
		return nil, errors.New("archive description service dependencies are required")
	}
	return &LocalService{
		nodeID: nodeID, state: state, prompts: prompts,
		archives: archives, generator: generator,
	}, nil
}

func (s *LocalService) Generate(ctx context.Context, request Request) (Result, error) {
	if request.NodeID != s.nodeID || request.Session.NodeID != s.nodeID ||
		request.Session.SessionID == "" || request.ExpectedRevision == 0 {
		return Result{}, domain.ErrInvalidState
	}
	state := s.state.State()
	if state == nil {
		return Result{}, domain.ErrInvalidState
	}
	session, ok := state.Sessions[request.Session.Key()]
	if !ok || session.State != domain.SessionArchived || session.ArchiveID != request.ArchiveID ||
		session.Revision != request.ExpectedRevision || session.DescriptionVersion >= domain.ArchiveDescriptionVersion {
		return Result{}, domain.ErrStaleOperation
	}
	providerSessionID := session.ProviderSessionID
	var err error
	if providerSessionID == "" {
		providerSessionID, err = s.discoverLegacyProviderSession(ctx, session)
	}
	var prompts []string
	if providerSessionID != "" {
		prompts, err = s.prompts.ReadFirstUserTexts(ctx, transcript.Request{
			Backend: transcript.Backend(session.Backend), ProviderSessionID: providerSessionID,
			Workdir: session.Workdir,
		}, 3)
	} else if err == nil {
		err = transcript.ErrTranscriptNotFound
	}
	if err != nil || len(prompts) == 0 {
		var archivedPrompts []string
		var archivePromptErr error
		if session.ArchiveReady && session.ArchiveID != "" {
			archivedPrompts, archivePromptErr = s.archives.ReadArchivedInitialUserPrompts(ctx, session)
		}
		if archivePromptErr == nil && len(archivedPrompts) > 0 {
			prompts = archivedPrompts
		} else if session.ArchiveReady && session.ArchiveID != "" {
			events, archiveErr := s.archives.ReadArchivedTranscript(ctx, session)
			if archiveErr != nil {
				return Result{}, errors.Join(err, archivePromptErr, archiveErr)
			}
			prompts = firstUserPrompts(events, 3)
		}
	}
	if len(prompts) == 0 {
		if IsLegacyEmptyCandidate(session) {
			return Result{Empty: true}, nil
		}
		return Result{}, transcript.ErrTranscriptNotFound
	}
	lines, err := s.generator.Generate(ctx, session.Backend, prompts)
	if err != nil {
		return Result{}, err
	}
	return Result{Lines: lines}, nil
}

const (
	legacyDiscoveryPageSize  = 32
	legacyDiscoveryLimit     = 512
	legacyCreatedAtTolerance = 2 * time.Second
)

func (s *LocalService) discoverLegacyProviderSession(
	ctx context.Context,
	session domain.Session,
) (string, error) {
	if session.CreatedAt.IsZero() || strings.TrimSpace(session.Workdir) == "" {
		return "", transcript.ErrTranscriptNotFound
	}
	matched := ""
	for offset := 0; offset < legacyDiscoveryLimit; offset += legacyDiscoveryPageSize {
		var discovery transcript.Discovery
		var err error
		if offset == 0 {
			discovery, err = s.prompts.DiscoverFresh(
				ctx, transcript.Backend(session.Backend), session.Workdir,
				offset, legacyDiscoveryPageSize,
			)
		} else {
			discovery, err = s.prompts.Discover(
				ctx, transcript.Backend(session.Backend), session.Workdir,
				offset, legacyDiscoveryPageSize,
			)
		}
		if err != nil {
			return "", err
		}
		for _, candidate := range discovery.Candidates {
			if candidate.CreatedAt.IsZero() ||
				absDuration(candidate.CreatedAt.Sub(session.CreatedAt)) > legacyCreatedAtTolerance {
				continue
			}
			if matched != "" && matched != candidate.ProviderSessionID {
				return "", transcript.ErrTranscriptNotFound
			}
			matched = candidate.ProviderSessionID
		}
		if offset+legacyDiscoveryPageSize >= discovery.Total {
			break
		}
	}
	if matched == "" {
		return "", transcript.ErrTranscriptNotFound
	}
	return matched, nil
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

// IsLegacyEmptyCandidate deliberately has a much narrower contract
// than "the transcript reader returned no rows". Current sessions carry the
// durable request tracker and are discarded before archival. Only legacy
// records with no request, provider binding, generated name, or operation can
// be purged after their native archive was also read successfully above.
func IsLegacyEmptyCandidate(session domain.Session) bool {
	return session.ArchiveReady && session.ArchiveID != "" &&
		!session.UserRequestTracked && !session.UserRequestSeen &&
		session.ProviderSessionID == "" && strings.TrimSpace(session.Name) == "" &&
		session.LastOperation == nil
}

func firstUserPrompts(events []transcript.Event, limit int) []string {
	result := make([]string, 0, limit)
	for _, event := range events {
		if event.Kind != transcript.EventUserText || strings.TrimSpace(event.Text) == "" {
			continue
		}
		result = append(result, event.Text)
		if len(result) == limit {
			break
		}
	}
	return result
}

type Router struct {
	localNodeID domain.NodeID
	local       Service
	remote      Service
}

func NewRouter(localNodeID domain.NodeID, local, remote Service) (*Router, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("archive description router dependencies are required")
	}
	return &Router{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *Router) Generate(ctx context.Context, request Request) (Result, error) {
	if request.NodeID == r.localNodeID {
		return r.local.Generate(ctx, request)
	}
	return r.remote.Generate(ctx, request)
}
