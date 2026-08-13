package sessionstart

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

type Leadership interface{ IsLeader() bool }

var errProviderBindingPending = errors.New("provider binding is pending")

type Controller struct {
	application  *application.Service
	state        StateReader
	router       Service
	leadership   Leadership
	interval     time.Duration
	startTimeout time.Duration
}

func NewController(
	app *application.Service,
	state StateReader,
	router Service,
	leadership Leadership,
) (*Controller, error) {
	if app == nil || state == nil || router == nil || leadership == nil {
		return nil, errors.New("session start controller dependencies are required")
	}
	return &Controller{
		application: app, state: state, router: router, leadership: leadership,
		interval: time.Second, startTimeout: 2 * time.Minute,
	}, nil
}

func (c *Controller) Browse(ctx context.Context, actor application.Principal, nodeID domain.NodeID, path string) (BrowseResult, error) {
	return c.router.Browse(ctx, BrowseRequest{ActorID: actor.UserID, NodeID: nodeID, Path: path})
}

func (c *Controller) Discover(
	ctx context.Context,
	actor application.Principal,
	nodeID domain.NodeID,
	backend, workdir string,
	offset, limit int,
) (ProviderPage, error) {
	discovery, err := c.router.Discover(ctx, DiscoverRequest{
		ActorID: actor.UserID, NodeID: nodeID, Backend: backend, Workdir: workdir,
		Offset: offset, Limit: limit,
	})
	if err != nil {
		return ProviderPage{}, err
	}
	result := make([]ProviderCandidate, len(discovery.Candidates))
	for index, item := range discovery.Candidates {
		result[index] = ProviderCandidate{ID: item.ProviderSessionID, Summary: item.Summary, UpdatedAt: item.UpdatedAt}
	}
	return ProviderPage{Items: result, Total: discovery.Total}, nil
}

type ProviderCandidate struct {
	ID        string
	Summary   string
	UpdatedAt time.Time
}

type ProviderPage struct {
	Items []ProviderCandidate
	Total int
}

func (c *Controller) Create(
	ctx context.Context,
	actor application.Principal,
	request application.CreateSessionRequest,
) (domain.Session, error) {
	session, err := c.application.CreateSession(ctx, actor, request)
	if err != nil {
		return domain.Session{}, err
	}
	// Creation intent is already durable. A transient target failure is retried
	// by Run, so the Telegram card can appear immediately in starting state.
	_ = c.provision(ctx, session)
	return session, nil
}

func (c *Controller) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if c.leadership.IsLeader() {
				c.reconcile(ctx)
			}
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) {
	state := c.state.State()
	sessions := make([]domain.Session, 0)
	for _, session := range state.Sessions {
		if session.IsLive() && session.RuntimePhase == domain.RuntimeStarting {
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt.Before(sessions[j].CreatedAt) })
	for _, session := range sessions {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.provision(requestCtx, session)
		cancel()
		if err != nil && !errors.Is(err, errProviderBindingPending) &&
			time.Since(session.CreatedAt) >= c.startTimeout {
			archiveScope := fmt.Sprintf("session-start-failed-%s-%d", session.Ref().Key(), session.Revision)
			archiveCtx := application.WithOperationScope(ctx, archiveScope)
			_ = c.application.ArchiveFailedStart(archiveCtx, session)
		}
	}
	c.bindMissingProviders(ctx)
}

func (c *Controller) provision(ctx context.Context, session domain.Session) error {
	request := ProvisionRequest{ActorID: session.OwnerID, Session: session.Ref()}
	if err := c.router.Provision(ctx, request); err != nil {
		return err
	}
	if err := c.bindProvider(ctx, session); err != nil {
		return err
	}
	latest, ok := c.state.State().Sessions[session.Ref().Key()]
	if !ok || latest.RuntimePhase != domain.RuntimeStarting {
		return nil
	}
	readyPhase := domain.RuntimeIdle
	if latest.LastOperation != nil && latest.LastOperation.Action == domain.ActionSendInput &&
		latest.LastOperation.Status == domain.OperationQueued {
		readyPhase = domain.RuntimeRunning
	}
	readyScope := fmt.Sprintf("session-start-ready-%s-%d", session.Ref().Key(), latest.Revision)
	stateCtx := application.WithOperationScope(ctx, readyScope)
	if err := c.application.PublishSessionRuntime(stateCtx, latest, readyPhase, nil); err != nil {
		return err
	}
	current := c.state.State().Sessions[session.Ref().Key()]
	if current.RuntimePhase == domain.RuntimeIdle && current.LastOperation != nil &&
		current.LastOperation.Action == domain.ActionSendInput &&
		current.LastOperation.Status == domain.OperationQueued {
		promoteScope := fmt.Sprintf("session-start-input-%s-%d", session.Ref().Key(), current.Revision)
		promoteCtx := application.WithOperationScope(ctx, promoteScope)
		return c.application.PublishSessionRuntime(promoteCtx, current, domain.RuntimeRunning, nil)
	}
	return nil
}

func (c *Controller) bindMissingProviders(ctx context.Context) {
	state := c.state.State()
	for _, session := range state.Sessions {
		if !session.IsLive() || !discoverableBackend(session.Backend) || session.ProviderSessionID != "" {
			continue
		}
		_ = c.bindProvider(ctx, session)
	}
}

func (c *Controller) bindProvider(ctx context.Context, session domain.Session) error {
	if !discoverableBackend(session.Backend) || session.ProviderSessionID != "" {
		return nil
	}
	discovery, err := c.router.Discover(ctx, DiscoverRequest{
		ActorID: session.OwnerID, NodeID: session.NodeID, Backend: session.Backend,
		Workdir: session.Workdir, Limit: 8, After: session.ProviderBindingSince,
	})
	if err != nil {
		return err
	}
	used := make(map[string]bool)
	for _, current := range c.state.State().Sessions {
		if strings.EqualFold(current.Backend, session.Backend) && current.ProviderSessionID != "" {
			used[current.ProviderSessionID] = true
		}
	}
	for _, item := range discovery.Candidates {
		if used[item.ProviderSessionID] {
			continue
		}
		latest := c.state.State().Sessions[session.Ref().Key()]
		bindScope := fmt.Sprintf("bind-provider-%s-%d", session.Ref().Key(), latest.Revision)
		bindCtx := application.WithOperationScope(ctx, bindScope)
		if err := c.application.BindProviderSession(
			bindCtx, application.Principal{UserID: session.OwnerID}, latest, item.ProviderSessionID,
		); err != nil {
			return err
		}
		return nil
	}
	return errProviderBindingPending
}

func discoverableBackend(backend string) bool {
	return strings.EqualFold(backend, "claude") || strings.EqualFold(backend, "codex")
}
