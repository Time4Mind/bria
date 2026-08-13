package sessionstart

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
	"github.com/Time4Mind/bria/internal/workspace"
)

type StateReader interface{ State() *domain.State }

type Local struct {
	nodeID      domain.NodeID
	state       StateReader
	browser     *workspace.Browser
	transcripts *transcript.Reader
	runtime     *runtimehost.TmuxRecoveryRuntime
	executor    *runtimehost.LocalExecutor
}

func NewLocal(
	nodeID domain.NodeID,
	state StateReader,
	browser *workspace.Browser,
	transcripts *transcript.Reader,
	runtime *runtimehost.TmuxRecoveryRuntime,
	executor *runtimehost.LocalExecutor,
) (*Local, error) {
	if nodeID == "" || state == nil || browser == nil || transcripts == nil || runtime == nil || executor == nil {
		return nil, errors.New("session start dependencies are required")
	}
	return &Local{nodeID: nodeID, state: state, browser: browser, transcripts: transcripts, runtime: runtime, executor: executor}, nil
}

func (l *Local) Browse(_ context.Context, request BrowseRequest) (BrowseResult, error) {
	if err := l.authorize(request.ActorID, request.NodeID); err != nil {
		return BrowseResult{}, err
	}
	path := strings.TrimSpace(request.Path)
	if path == "" {
		path = l.browser.Home()
	}
	path, err := l.browser.Resolve(path)
	if err != nil {
		return BrowseResult{}, err
	}
	directories, err := l.browser.List(path)
	if err != nil {
		return BrowseResult{}, err
	}
	parent, _ := workspace.Parent(path)
	return BrowseResult{Path: filepath.Clean(path), Parent: parent, Directories: directories}, nil
}

func (l *Local) Discover(ctx context.Context, request DiscoverRequest) (transcript.Discovery, error) {
	if err := l.authorize(request.ActorID, request.NodeID); err != nil {
		return transcript.Discovery{}, err
	}
	discovery, err := l.transcripts.Discover(
		ctx, transcript.Backend(strings.ToLower(request.Backend)), request.Workdir,
		request.Offset, request.Limit,
	)
	if err != nil {
		return transcript.Discovery{}, err
	}
	if request.After.IsZero() {
		return discovery, nil
	}
	filtered := discovery.Candidates[:0]
	for _, candidate := range discovery.Candidates {
		if !candidate.UpdatedAt.Before(request.After) {
			filtered = append(filtered, candidate)
		}
	}
	discovery.Candidates = filtered
	discovery.Total = len(filtered)
	return discovery, nil
}

func (l *Local) Provision(ctx context.Context, request ProvisionRequest) error {
	if request.Session.NodeID != l.nodeID {
		return domain.ErrNotFound
	}
	state := l.state.State()
	session, ok := state.Sessions[request.Session.Key()]
	if !ok || session.OwnerID != request.ActorID || !session.IsLive() ||
		session.RuntimePhase != domain.RuntimeStarting {
		return domain.ErrInvalidState
	}
	// The local process itself proves that this node is reachable. Replicated
	// status can briefly lag or say offline during a control-link interruption;
	// that must not prevent the owner node from materializing its own durable
	// starting intent.
	if _, exists := state.Nodes[session.NodeID]; !exists ||
		!state.CanAccessNode(request.ActorID, session.NodeID) {
		return domain.ErrNotFound
	}
	binding := runtimehost.RuntimeBinding{
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		Generation: session.RuntimeGeneration, Backend: session.Backend, Workdir: session.Workdir,
	}
	if err := l.executor.Prepare(binding); err != nil {
		return err
	}
	target, err := l.runtime.Start(ctx, session)
	if err != nil {
		return err
	}
	binding.TmuxTarget = target
	return l.executor.Register(binding)
}

func (l *Local) authorize(actor domain.UserID, nodeID domain.NodeID) error {
	state := l.state.State()
	node, ok := state.Nodes[nodeID]
	if nodeID != l.nodeID || !ok || node.Status != domain.NodeOnline || !state.CanAccessNode(actor, nodeID) {
		return domain.ErrNotFound
	}
	return nil
}
