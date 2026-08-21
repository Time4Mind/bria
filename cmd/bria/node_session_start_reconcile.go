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

const localSessionStartInterval = time.Second

type localSessionStartState interface{ State() *domain.State }

type localSessionProvisioner interface {
	Provision(context.Context, sessionstart.ProvisionRequest) error
}

type localRuntimeUnregisterer interface {
	Unregister(string, string, uint64) error
}

// localSessionStartReconciler removes control-plane reachability from the
// critical path of starting a session. Every node owns its provider processes,
// so it may safely materialize only its own replicated starting intents.
type localSessionStartReconciler struct {
	nodeID      domain.NodeID
	state       localSessionStartState
	provisioner localSessionProvisioner
	runtimes    localRuntimeUnregisterer
	tracked     map[string]domain.Session
}

func newLocalSessionStartReconciler(
	nodeID string,
	state localSessionStartState,
	provisioner localSessionProvisioner,
	runtimes localRuntimeUnregisterer,
) (*localSessionStartReconciler, error) {
	if nodeID == "" || state == nil || provisioner == nil || runtimes == nil {
		return nil, fmt.Errorf("local session start dependencies are required")
	}
	return &localSessionStartReconciler{
		nodeID: domain.NodeID(nodeID), state: state, provisioner: provisioner,
		runtimes: runtimes, tracked: make(map[string]domain.Session),
	}, nil
}

func (r *localSessionStartReconciler) Reconcile(ctx context.Context) error {
	state := r.state.State()
	if state == nil {
		return fmt.Errorf("cluster state is unavailable")
	}
	starting := make(map[string]domain.Session)
	var firstErr error
	for _, session := range state.Sessions {
		if session.NodeID != r.nodeID || !session.IsLive() ||
			session.RuntimePhase != domain.RuntimeStarting {
			continue
		}
		starting[session.Ref().Key()] = session
		if err := r.provisioner.Provision(ctx, sessionstart.ProvisionRequest{
			ActorID: session.OwnerID, Session: session.Ref(),
		}); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("provision %s: %w", session.Ref().Key(), err)
		}
	}
	for key, previous := range r.tracked {
		if _, stillStarting := starting[key]; stillStarting {
			continue
		}
		current, exists := state.Sessions[key]
		if exists && current.IsLive() {
			continue
		}
		if err := r.runtimes.Unregister(
			string(previous.NodeID), string(previous.ID), previous.RuntimeGeneration,
		); err != nil && err != runtimehost.ErrRuntimeUnavailable && firstErr == nil {
			firstErr = fmt.Errorf("unregister failed start %s: %w", key, err)
		}
	}
	r.tracked = starting
	return firstErr
}

func runLocalSessionStartReconciler(
	ctx context.Context,
	nodeID string,
	state localSessionStartState,
	provisioner localSessionProvisioner,
	runtimes localRuntimeUnregisterer,
) {
	reconciler, err := newLocalSessionStartReconciler(nodeID, state, provisioner, runtimes)
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
