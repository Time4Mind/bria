package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

const (
	runtimeReconcileInterval = 5 * time.Second
	runtimeMissingThreshold  = 2
)

type runtimeExistence interface {
	TargetExists(context.Context, string) (bool, error)
}

type runtimeMissingApplier interface {
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
}

type runtimeRegistry interface {
	Register(runtimehost.RuntimeBinding) error
	Unregister(string, string, uint64) error
}

type runtimeMissingReconciler struct {
	nodeID      string
	tmuxSession string
	state       nodecontrol.StateReader
	exists      runtimeExistence
	apply       runtimeMissingApplier
	runtimes    runtimeRegistry
	misses      map[string]int
	now         func() time.Time
	newID       func() (string, error)
}

func newRuntimeMissingReconciler(
	nodeConfig config.Config,
	state nodecontrol.StateReader,
	exists runtimeExistence,
	apply runtimeMissingApplier,
	runtimes runtimeRegistry,
) (*runtimeMissingReconciler, error) {
	if nodeConfig.NodeID == "" || nodeConfig.TmuxSession == "" || state == nil ||
		exists == nil || apply == nil || runtimes == nil {
		return nil, fmt.Errorf("runtime reconciliation dependencies are required")
	}
	return &runtimeMissingReconciler{
		nodeID: nodeConfig.NodeID, tmuxSession: nodeConfig.TmuxSession,
		state: state, exists: exists, apply: apply, runtimes: runtimes,
		misses: make(map[string]int), now: time.Now, newID: newOperationID,
	}, nil
}

func (r *runtimeMissingReconciler) Reconcile(ctx context.Context) error {
	state := r.state.State()
	if state == nil {
		return fmt.Errorf("cluster state is unavailable")
	}
	live := make(map[string]bool)
	for _, session := range state.Sessions {
		if session.NodeID != domain.NodeID(r.nodeID) || !session.IsLive() ||
			session.RuntimePhase == domain.RuntimeStarting || session.ResumePending {
			continue
		}
		key := fmt.Sprintf("%s/%d/%d", session.Ref().Key(), session.RuntimeGeneration, session.Revision)
		live[key] = true
		target := runtimehost.TmuxTarget(r.tmuxSession, r.nodeID, string(session.ID))
		exists, err := r.exists.TargetExists(ctx, target)
		if err != nil {
			continue
		}
		if exists {
			binding := runtimehost.RuntimeBinding{
				NodeID: r.nodeID, SessionID: string(session.ID),
				Generation: session.RuntimeGeneration, TmuxTarget: target,
				Backend: session.Backend, Workdir: session.Workdir,
			}
			if err := r.runtimes.Register(binding); err != nil && err != runtimehost.ErrStaleRuntime {
				return fmt.Errorf("register existing runtime %s: %w", session.Ref().Key(), err)
			}
			delete(r.misses, key)
			continue
		}
		r.misses[key]++
		if r.misses[key] < runtimeMissingThreshold {
			continue
		}
		if err := r.archiveMissing(ctx, session); err != nil {
			return err
		}
		delete(r.misses, key)
	}
	for key := range r.misses {
		if !live[key] {
			delete(r.misses, key)
		}
	}
	return nil
}

func (r *runtimeMissingReconciler) archiveMissing(
	ctx context.Context,
	session domain.Session,
) error {
	operationID, err := r.newID()
	if err != nil {
		return err
	}
	archiveID := clusterstate.MissingArchiveID(operationID)
	command, err := clusterstate.NewCommand(
		operationID, clusterstate.CommandMarkMissing, r.now(),
		clusterstate.MarkMissing{
			Session: session.Ref(), ArchiveID: archiveID,
			ExpectedGeneration: session.RuntimeGeneration,
			ExpectedRevision:   session.Revision, CheckVersion: true,
		},
	)
	if err != nil {
		return err
	}
	result, err := r.apply.Apply(ctx, command)
	if err != nil {
		return fmt.Errorf("archive missing runtime %s: %w", session.Ref().Key(), err)
	}
	if err := result.Err(); err != nil {
		return fmt.Errorf("archive missing runtime %s: %w", session.Ref().Key(), err)
	}
	if err := r.runtimes.Unregister(
		r.nodeID, string(session.ID), session.RuntimeGeneration,
	); err != nil && err != runtimehost.ErrRuntimeUnavailable {
		return fmt.Errorf("unregister missing runtime %s: %w", session.Ref().Key(), err)
	}
	return nil
}

func runLocalRuntimeReconciler(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
	client *nodecontrol.Client,
	executor *runtimehost.LocalExecutor,
	driver *runtimehost.TmuxDriver,
) {
	remote, err := nodecontrol.NewRemoteRecoveryApplier(nodeConfig.NodeID, node, client)
	if err != nil {
		processlog.Criticalf("bria runtime reconcile: %v", err)
		return
	}
	reconciler, err := newRuntimeMissingReconciler(
		nodeConfig, node.State(), driver, remote, executor,
	)
	if err != nil {
		processlog.Criticalf("bria runtime reconcile: %v", err)
		return
	}
	ticker := time.NewTicker(runtimeReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconciler.Reconcile(ctx); err != nil && ctx.Err() == nil {
				processlog.Criticalf("bria runtime reconcile: %v", err)
			}
		}
	}
}
