package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

const (
	providerBindingReconcileInterval = 5 * time.Second
	providerBindingFullInterval      = time.Hour
	providerBindingMissingGrace      = 24 * time.Hour
)

type providerBindingState interface{ State() *domain.State }

type providerBindingStore interface {
	Snapshot() ([]providerbinding.Record, error)
	Sweep(providerbinding.SweepInput) error
}

type providerBindingTargetProbe interface {
	TargetExists(context.Context, string) (bool, error)
}

type providerBindingReconciler struct {
	nodeID      domain.NodeID
	tmuxSession string
	state       providerBindingState
	store       providerBindingStore
	targets     providerBindingTargetProbe
	now         func() time.Time
}

func newProviderBindingReconciler(
	nodeConfig config.Config,
	state providerBindingState,
	store providerBindingStore,
	targets providerBindingTargetProbe,
) (*providerBindingReconciler, error) {
	if nodeConfig.NodeID == "" || nodeConfig.TmuxSession == "" || state == nil ||
		store == nil || targets == nil {
		return nil, fmt.Errorf("provider binding reconciliation dependencies are required")
	}
	return &providerBindingReconciler{
		nodeID: domain.NodeID(nodeConfig.NodeID), tmuxSession: nodeConfig.TmuxSession,
		state: state, store: store, targets: targets, now: time.Now,
	}, nil
}

func (r *providerBindingReconciler) Reconcile(ctx context.Context, full bool) error {
	records, err := r.store.Snapshot()
	if err != nil || len(records) == 0 {
		return err
	}
	state := r.state.State()
	if state == nil {
		return fmt.Errorf("cluster state is unavailable")
	}
	input := providerbinding.SweepInput{}
	if full {
		input.MissingBefore = r.now().UTC().Add(-providerBindingMissingGrace)
	}
	var firstErr error
	for _, record := range records {
		ref := domain.SessionRef{
			NodeID: domain.NodeID(record.NodeID), SessionID: domain.SessionID(record.SessionID),
		}
		session, exists := state.Sessions[ref.Key()]
		if !exists || session.NodeID != r.nodeID {
			if !full {
				input.KeepRefs = append(input.KeepRefs, ref)
			}
			continue
		}
		if session.IsLive() || session.State == domain.SessionDiscarding ||
			session.State != domain.SessionArchived || !session.ArchiveReady {
			input.KeepRefs = append(input.KeepRefs, ref)
			continue
		}
		target := runtimehost.TmuxTarget(
			r.tmuxSession, string(session.NodeID), string(session.ID),
		)
		targetExists, targetErr := r.targets.TargetExists(ctx, target)
		if targetErr != nil {
			input.KeepRefs = append(input.KeepRefs, ref)
			if firstErr == nil {
				firstErr = fmt.Errorf("inspect archived binding target %s: %w", ref.Key(), targetErr)
			}
			continue
		}
		input.Archived = append(input.Archived, providerbinding.SweepArchived{
			Ref: ref, RuntimeGeneration: session.RuntimeGeneration, TargetAbsent: !targetExists,
		})
	}
	if err := r.store.Sweep(input); err != nil {
		return err
	}
	return firstErr
}

func runProviderBindingReconciler(
	ctx context.Context,
	nodeConfig config.Config,
	state providerBindingState,
	store providerBindingStore,
	targets providerBindingTargetProbe,
) {
	reconciler, err := newProviderBindingReconciler(nodeConfig, state, store, targets)
	if err != nil {
		lastError := ""
		logPeriodicReconcile("provider binding reconcile", err, &lastError)
		return
	}
	fast := time.NewTicker(providerBindingReconcileInterval)
	defer fast.Stop()
	full := time.NewTicker(providerBindingFullInterval)
	defer full.Stop()
	lastError := ""
	reconcileAndLog := func(fullPass bool) {
		err := reconciler.Reconcile(ctx, fullPass)
		if ctx.Err() == nil {
			logPeriodicReconcile("provider binding reconcile", err, &lastError)
		}
	}
	reconcileAndLog(true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			reconcileAndLog(false)
		case <-full.C:
			reconcileAndLog(true)
		}
	}
}
