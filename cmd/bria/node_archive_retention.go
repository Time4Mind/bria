package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
)

const archiveRetentionInterval = time.Hour

type archiveRetentionNode interface {
	IsLeader() bool
	State() *clusterstate.Machine
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
}

func reconcileArchiveRetention(ctx context.Context, node archiveRetentionNode, now time.Time) (int, error) {
	if !node.IsLeader() {
		return 0, nil
	}
	state := node.State().State()
	candidates := make([]domain.Session, 0)
	for _, session := range state.Sessions {
		if session.State != domain.SessionArchived || !session.ArchiveReady ||
			session.ArchiveID == "" {
			continue
		}
		preferences, ok := state.Preferences[session.OwnerID]
		if !ok {
			preferences = domain.DefaultUserPreferences()
		}
		dueAt, finite, err := archive.PolicyFromPreferences(preferences).Retention.DueAt(session.ArchivedAt)
		if err != nil {
			return 0, fmt.Errorf("plan archive purge %s: %w", session.Ref().Key(), err)
		}
		if finite && !now.Before(dueAt) {
			candidates = append(candidates, session)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].ArchivedAt.Equal(candidates[j].ArchivedAt) {
			return candidates[i].ArchivedAt.Before(candidates[j].ArchivedAt)
		}
		return candidates[i].Ref().Key() < candidates[j].Ref().Key()
	})
	var joined error
	purged := 0
	for _, session := range candidates {
		if !node.IsLeader() {
			break
		}
		command, err := clusterstate.NewCommand(
			archivePurgeOperationID(session), clusterstate.CommandPurgeSession, now,
			clusterstate.PurgeSession{
				Session: session.Ref(), ArchiveID: session.ArchiveID,
				ExpectedRevision: session.Revision,
			},
		)
		if err == nil {
			var result clusterstate.Result
			result, err = node.Apply(ctx, command)
			if err == nil {
				err = result.Err()
			}
		}
		if err == nil {
			purged++
		} else if archivePurgeStillRequired(node.State().State(), session) {
			joined = errors.Join(joined, fmt.Errorf("purge archive %s: %w", session.ArchiveID, err))
		}
	}
	return purged, joined
}

func archivePurgeStillRequired(state *domain.State, planned domain.Session) bool {
	current, ok := state.Sessions[planned.Ref().Key()]
	return ok && current.State == domain.SessionArchived && current.ArchiveReady &&
		current.ArchiveID == planned.ArchiveID
}

func archivePurgeOperationID(session domain.Session) string {
	digest := sha256.Sum256([]byte(session.Ref().Key() + "\x00" + session.ArchiveID))
	return fmt.Sprintf("archive-purge-%x", digest[:16])
}

func runArchiveRetentionReconciler(ctx context.Context, node archiveRetentionNode) {
	ticker := time.NewTicker(archiveRetentionInterval)
	defer ticker.Stop()
	lastError := ""
	reconcile := func() {
		purged, err := reconcileArchiveRetention(ctx, node, time.Now().UTC())
		if ctx.Err() == nil {
			logPeriodicReconcile("archive retention", err, &lastError)
			if purged > 0 {
				processlog.Servicef("bria archive retention: purged=%d", purged)
			}
		}
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

type archiveBundleStore interface {
	ReadyArchiveIDs() ([]string, error)
	DeleteArchive(context.Context, string) error
}

type archiveBindingStore interface {
	DeleteIfGeneration(domain.SessionRef, uint64) error
}

type localArchivePurgeReconciler struct {
	nodeID   domain.NodeID
	state    archiveStateReader
	archives archiveBundleStore
	bindings archiveBindingStore
	cleaned  map[string]bool
}

func (r *localArchivePurgeReconciler) Reconcile(ctx context.Context) error {
	state := r.state.State()
	if state == nil {
		return errors.New("cluster state is unavailable")
	}
	retainedCleaned := make(map[string]bool)
	for key, tombstone := range state.SessionTombstones {
		cleanedKey := key + "\x00" + tombstone.ArchiveID
		if r.cleaned[cleanedKey] {
			retainedCleaned[cleanedKey] = true
		}
	}
	r.cleaned = retainedCleaned
	activeArchives := make(map[string]bool)
	deletedArchives := make(map[string]bool)
	for _, session := range state.Sessions {
		if session.NodeID == r.nodeID && session.ArchiveID != "" {
			activeArchives[session.ArchiveID] = true
		}
	}
	var joined error
	for key, tombstone := range state.SessionTombstones {
		if tombstone.Session.NodeID != r.nodeID || r.cleaned[key+"\x00"+tombstone.ArchiveID] {
			continue
		}
		if activeArchives[tombstone.ArchiveID] {
			joined = errors.Join(joined, fmt.Errorf(
				"preserve archive bundle %s: still referenced by a session", tombstone.ArchiveID,
			))
			continue
		}
		if err := r.bindings.DeleteIfGeneration(tombstone.Session, tombstone.RuntimeGeneration); err != nil {
			joined = errors.Join(joined, fmt.Errorf("delete provider binding %s: %w", key, err))
			continue
		}
		if tombstone.ArchiveID != "" {
			if err := r.archives.DeleteArchive(ctx, tombstone.ArchiveID); err != nil {
				joined = errors.Join(joined, fmt.Errorf("delete archive bundle %s: %w", tombstone.ArchiveID, err))
				continue
			}
		}
		r.cleaned[key+"\x00"+tombstone.ArchiveID] = true
		deletedArchives[tombstone.ArchiveID] = true
	}
	ready, err := r.archives.ReadyArchiveIDs()
	if err != nil {
		return errors.Join(joined, fmt.Errorf("list archive bundles: %w", err))
	}
	// Re-read after listing. A close may publish a ready bundle after the first
	// snapshot; deleting from that stale view would race a valid new archive.
	current := r.state.State()
	if current == nil {
		return errors.Join(joined, errors.New("cluster state is unavailable"))
	}
	activeArchives = make(map[string]bool)
	for _, session := range current.Sessions {
		if session.NodeID == r.nodeID && session.ArchiveID != "" {
			activeArchives[session.ArchiveID] = true
		}
	}
	for _, archiveID := range ready {
		if activeArchives[archiveID] || deletedArchives[archiveID] {
			continue
		}
		if err := r.archives.DeleteArchive(ctx, archiveID); err != nil {
			joined = errors.Join(joined, fmt.Errorf("delete orphan archive bundle %s: %w", archiveID, err))
		}
	}
	return joined
}

func runLocalArchivePurgeReconciler(
	ctx context.Context,
	nodeID domain.NodeID,
	state archiveStateReader,
	archives archiveBundleStore,
	bindings archiveBindingStore,
) {
	reconciler := &localArchivePurgeReconciler{
		nodeID: nodeID, state: state, archives: archives, bindings: bindings,
		cleaned: make(map[string]bool),
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastError := ""
	reconcile := func() {
		err := reconciler.Reconcile(ctx)
		if ctx.Err() == nil {
			logPeriodicReconcile("archive purge", err, &lastError)
		}
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
