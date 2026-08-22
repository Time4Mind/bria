package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessionstart"
)

const (
	localSessionStartInterval      = time.Second
	localSessionStartCleanupGrace  = 5 * time.Minute
	localSessionStartConfirmations = 2
)

type localSessionStartState interface{ State() *domain.State }

type localSessionProvisioner interface {
	Provision(context.Context, sessionstart.ProvisionRequest) error
}

type localRuntimeUnregisterer interface {
	Unregister(string, string, uint64) error
}

type localStartRuntimeTarget interface {
	TargetExists(context.Context, string) (bool, error)
	Close(context.Context, string) error
}

type terminalStartRuntime struct {
	session       domain.Session
	notBefore     time.Time
	confirmations int
}

// localSessionStartReconciler removes control-plane reachability from the
// critical path of starting a session. Every node owns its provider processes,
// so it may safely materialize only its own replicated starting intents.
type localSessionStartReconciler struct {
	nodeID      domain.NodeID
	tmuxSession string
	state       localSessionStartState
	provisioner localSessionProvisioner
	runtimes    localRuntimeUnregisterer
	targets     localStartRuntimeTarget
	tracked     map[string]domain.Session
	terminal    map[string]terminalStartRuntime
	cleaned     map[string]uint64
	now         func() time.Time
}

func newLocalSessionStartReconciler(
	nodeID string,
	tmuxSession string,
	state localSessionStartState,
	provisioner localSessionProvisioner,
	runtimes localRuntimeUnregisterer,
	targets localStartRuntimeTarget,
) (*localSessionStartReconciler, error) {
	if nodeID == "" || tmuxSession == "" || state == nil || provisioner == nil ||
		runtimes == nil || targets == nil {
		return nil, fmt.Errorf("local session start dependencies are required")
	}
	return &localSessionStartReconciler{
		nodeID: domain.NodeID(nodeID), tmuxSession: tmuxSession,
		state: state, provisioner: provisioner, runtimes: runtimes, targets: targets,
		tracked:  make(map[string]domain.Session),
		terminal: make(map[string]terminalStartRuntime), now: time.Now,
		cleaned: make(map[string]uint64),
	}, nil
}

func (r *localSessionStartReconciler) Reconcile(ctx context.Context) error {
	state := r.state.State()
	if state == nil {
		return fmt.Errorf("cluster state is unavailable")
	}
	if r.cleaned == nil {
		r.cleaned = make(map[string]uint64)
	}
	retainedCleaned := make(map[string]uint64)
	for key, generation := range r.cleaned {
		if session, ok := state.Sessions[key]; ok && session.State == domain.SessionArchived &&
			session.RuntimeGeneration == generation {
			retainedCleaned[key] = generation
		}
	}
	r.cleaned = retainedCleaned
	starting := make(map[string]domain.Session)
	var firstErr error
	for _, session := range state.Sessions {
		if session.NodeID != r.nodeID || !session.IsLive() ||
			session.RuntimePhase != domain.RuntimeStarting {
			if session.NodeID == r.nodeID && session.State == domain.SessionArchived &&
				session.ArchiveReason == domain.ArchiveResumeFailed &&
				session.ArchiveID == "" && !session.ArchiveReady && !session.ProviderResume {
				key := session.Ref().Key()
				if r.cleaned[key] == session.RuntimeGeneration {
					continue
				}
				if _, tracked := r.terminal[key]; !tracked {
					notBefore := session.ArchivedAt.Add(localSessionStartCleanupGrace)
					if session.ArchivedAt.IsZero() {
						notBefore = r.now().UTC().Add(localSessionStartCleanupGrace)
					}
					r.terminal[key] = terminalStartRuntime{
						session: session, notBefore: notBefore,
					}
				}
			}
			continue
		}
		delete(r.cleaned, session.Ref().Key())
		starting[session.Ref().Key()] = session
		if err := r.provisioner.Provision(ctx, sessionstart.ProvisionRequest{
			ActorID: session.OwnerID, Session: session.Ref(),
		}); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("provision %s: %w", session.Ref().Key(), err)
		}
	}
	now := r.now().UTC()
	for key, previous := range r.tracked {
		if _, stillStarting := starting[key]; stillStarting {
			continue
		}
		current, exists := state.Sessions[key]
		if exists && current.IsLive() {
			delete(r.terminal, key)
			continue
		}
		if _, tracked := r.terminal[key]; !tracked {
			r.terminal[key] = terminalStartRuntime{
				session: previous, notBefore: now.Add(localSessionStartCleanupGrace),
			}
		}
	}
	for key, candidate := range r.terminal {
		currentState := r.state.State()
		if currentState == nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("cluster state is unavailable")
			}
			break
		}
		current, exists := currentState.Sessions[key]
		if exists && (current.IsLive() || current.RuntimeGeneration > candidate.session.RuntimeGeneration) {
			delete(r.terminal, key)
			continue
		}
		if now.Before(candidate.notBefore) {
			continue
		}
		target := runtimehost.TmuxTarget(
			r.tmuxSession, string(candidate.session.NodeID), string(candidate.session.ID),
		)
		targetExists, err := r.targets.TargetExists(ctx, target)
		if err != nil {
			candidate.confirmations = 0
			r.terminal[key] = candidate
			if firstErr == nil {
				firstErr = fmt.Errorf("inspect terminal start %s: %w", key, err)
			}
			continue
		}
		if targetExists && candidate.confirmations+1 < localSessionStartConfirmations {
			candidate.confirmations++
			r.terminal[key] = candidate
			continue
		}
		// The tmux probe can be slow. Re-read durable state immediately before
		// destructive cleanup so a recovered/newer incarnation always wins.
		latestState := r.state.State()
		if latestState == nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("cluster state is unavailable")
			}
			continue
		}
		latest, latestExists := latestState.Sessions[key]
		if latestExists && (latest.IsLive() || latest.RuntimeGeneration > candidate.session.RuntimeGeneration) {
			delete(r.terminal, key)
			continue
		}
		if targetExists {
			if err := r.targets.Close(ctx, target); err != nil {
				stillExists, inspectErr := r.targets.TargetExists(ctx, target)
				if inspectErr != nil || stillExists {
					candidate.confirmations = 0
					r.terminal[key] = candidate
					if firstErr == nil {
						firstErr = fmt.Errorf("close terminal start %s: %w", key, err)
					}
					continue
				}
			}
		}
		if err := r.runtimes.Unregister(
			string(candidate.session.NodeID), string(candidate.session.ID),
			candidate.session.RuntimeGeneration,
		); err != nil && err != runtimehost.ErrRuntimeUnavailable {
			if firstErr == nil {
				firstErr = fmt.Errorf("unregister terminal start %s: %w", key, err)
			}
			continue
		}
		delete(r.terminal, key)
		r.cleaned[key] = candidate.session.RuntimeGeneration
		processlog.Servicef(
			"bria local session start: outcome=terminal_runtime_cleaned ref=%q generation=%d target_present=%t",
			key, candidate.session.RuntimeGeneration, targetExists,
		)
	}
	r.tracked = starting
	return firstErr
}

func runLocalSessionStartReconciler(
	ctx context.Context,
	nodeID string,
	tmuxSession string,
	state localSessionStartState,
	provisioner localSessionProvisioner,
	runtimes localRuntimeUnregisterer,
	targets localStartRuntimeTarget,
) {
	reconciler, err := newLocalSessionStartReconciler(
		nodeID, tmuxSession, state, provisioner, runtimes, targets,
	)
	if err != nil {
		processlog.Failuref(
			processlog.Critical, processlog.FailureInvalidState,
			"bria local session start: outcome=initialization_failed",
		)
		return
	}
	ticker := time.NewTicker(localSessionStartInterval)
	defer ticker.Stop()
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := reconciler.Reconcile(ctx)
			detail := ""
			if err != nil {
				detail = err.Error()
			}
			if detail != "" && detail != lastError {
				processlog.Failuref(
					processlog.Critical, processlog.FailureDependency,
					"bria local session start: outcome=reconcile_failed",
				)
			}
			lastError = detail
		}
	}
}
