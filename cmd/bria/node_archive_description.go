package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/sessiondescription"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	archiveDescriptionInterval = 2 * time.Second
	archiveDescriptionRetry    = time.Minute
	archiveDescriptionNoSource = 6 * time.Hour
)

type archiveDescriptionNode interface {
	IsLeader() bool
	State() *clusterstate.Machine
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
}

type archiveDescriptionReconciler struct {
	node         archiveDescriptionNode
	descriptions sessiondescription.Service
	now          func() time.Time
	retryAt      map[string]time.Time
	failed       map[string]bool
}

func newArchiveDescriptionReconciler(
	node archiveDescriptionNode,
	descriptions sessiondescription.Service,
) *archiveDescriptionReconciler {
	return &archiveDescriptionReconciler{
		node: node, descriptions: descriptions, now: time.Now,
		retryAt: make(map[string]time.Time), failed: make(map[string]bool),
	}
}

func (r *archiveDescriptionReconciler) reconcile(ctx context.Context) (bool, error) {
	if !r.node.IsLeader() {
		return false, nil
	}
	state := r.node.State().State()
	if state == nil {
		return false, domain.ErrInvalidState
	}
	candidates := make([]domain.Session, 0)
	liveKeys := make(map[string]bool)
	for _, session := range state.Sessions {
		if session.State == domain.SessionArchived &&
			session.DescriptionVersion < domain.ArchiveDescriptionVersion {
			candidates = append(candidates, session)
			liveKeys[session.Ref().Key()+"\x00"+session.ArchiveID] = true
		}
	}
	for key := range r.retryAt {
		if !liveKeys[key] {
			delete(r.retryAt, key)
			delete(r.failed, key)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].ArchivedAt.Equal(candidates[j].ArchivedAt) {
			return candidates[i].ArchivedAt.After(candidates[j].ArchivedAt)
		}
		return candidates[i].Ref().Key() < candidates[j].Ref().Key()
	})
	now := r.now().UTC()
	for _, session := range candidates {
		key := session.Ref().Key() + "\x00" + session.ArchiveID
		if next := r.retryAt[key]; now.Before(next) {
			continue
		}
		result, err := r.descriptions.Generate(ctx, sessiondescription.Request{
			NodeID: session.NodeID, Session: session.Ref(), ArchiveID: session.ArchiveID,
			ExpectedRevision: session.Revision,
		})
		if err != nil {
			retry := archiveDescriptionRetry
			failureClass := processlog.FailureDependency
			outcome := "generate_failed"
			if errors.Is(err, transcript.ErrTranscriptNotFound) {
				retry = archiveDescriptionNoSource
				failureClass = processlog.FailureNotFound
				outcome = "source_missing"
			}
			r.retryAt[key] = now.Add(retry)
			if !r.failed[key] {
				r.failed[key] = true
				processlog.Failuref(
					processlog.Service, failureClass,
					"bria archive description: ref=%q outcome=%s retry_seconds=%d",
					session.Ref().Key(), outcome, int(retry/time.Second),
				)
			}
			return false, nil
		}
		currentState := r.node.State().State()
		if currentState == nil {
			return false, domain.ErrInvalidState
		}
		current, ok := currentState.Sessions[session.Ref().Key()]
		if !ok || current.State != domain.SessionArchived || current.ArchiveID != session.ArchiveID ||
			current.DescriptionVersion >= domain.ArchiveDescriptionVersion {
			delete(r.retryAt, key)
			delete(r.failed, key)
			return false, nil
		}
		var command clusterstate.Command
		if result.Empty {
			if len(result.Lines) != 0 || !sessiondescription.IsLegacyEmptyCandidate(current) {
				return false, domain.ErrInvalidState
			}
			command, err = clusterstate.NewCommand(
				archivePurgeOperationID(current), clusterstate.CommandPurgeSession, now,
				clusterstate.PurgeSession{
					Session: current.Ref(), ExpectedRevision: current.Revision,
					ArchiveID: current.ArchiveID,
				},
			)
		} else {
			command, err = clusterstate.NewCommand(
				archiveDescriptionOperationID(current, result.Lines),
				clusterstate.CommandSetArchiveDescription, now,
				clusterstate.SetArchiveDescription{
					Session: current.Ref(), ExpectedRevision: current.Revision,
					ArchiveID: current.ArchiveID, Lines: result.Lines,
					Version: domain.ArchiveDescriptionVersion,
				},
			)
		}
		if err == nil {
			var applied clusterstate.Result
			applied, err = r.node.Apply(ctx, command)
			if err == nil {
				err = applied.Err()
			}
		}
		if err != nil {
			r.retryAt[key] = now.Add(archiveDescriptionRetry)
			return false, err
		}
		if result.Empty {
			processlog.Servicef(
				"bria archive description: ref=%q outcome=empty_purged", current.Ref().Key(),
			)
		}
		if r.failed[key] {
			processlog.Servicef(
				"bria archive description: ref=%q outcome=recovered", current.Ref().Key(),
			)
		}
		delete(r.retryAt, key)
		delete(r.failed, key)
		return true, nil
	}
	return false, nil
}

func archiveDescriptionOperationID(session domain.Session, lines []string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(session.Ref().Key()))
	_, _ = hash.Write([]byte{'\x00'})
	_, _ = hash.Write([]byte(session.ArchiveID))
	_, _ = hash.Write([]byte(fmt.Sprintf("\x00%d\x00", session.Revision)))
	for _, line := range lines {
		_, _ = hash.Write([]byte(line))
		_, _ = hash.Write([]byte{'\x00'})
	}
	return fmt.Sprintf("archive-description-%x", hash.Sum(nil)[:16])
}

func runArchiveDescriptionReconciler(
	ctx context.Context,
	node archiveDescriptionNode,
	descriptions sessiondescription.Service,
) {
	reconciler := newArchiveDescriptionReconciler(node, descriptions)
	ticker := time.NewTicker(archiveDescriptionInterval)
	defer ticker.Stop()
	for {
		_, err := reconciler.reconcile(ctx)
		if err != nil && ctx.Err() == nil {
			processlog.Failuref(
				processlog.Service, processlog.FailureConsistency,
				"bria archive description: outcome=commit_failed",
			)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
