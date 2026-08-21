package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/localarchive"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

// reconcileLocalArchives closes the crash window between atomic artifact
// publication and runtime deactivation. Only an artifact whose identity and
// integrity verify is allowed to deactivate a leftover tmux window.
func reconcileLocalArchives(
	ctx context.Context,
	state *domain.State,
	nodeConfig config.Config,
	driver *runtimehost.TmuxDriver,
	archives *localarchive.Writer,
) error {
	for _, session := range state.Sessions {
		if session.NodeID != domain.NodeID(nodeConfig.NodeID) ||
			session.State != domain.SessionArchived || session.ArchiveReady ||
			session.ArchiveReason == "" || session.ArchiveID == "" {
			continue
		}
		if err := archives.Verify(ctx, session); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("verify archive %s: %w", session.ArchiveID, err)
			}
			if err := archives.Commit(ctx, archiveRuntimeRequest(session)); err != nil {
				return fmt.Errorf("commit archive %s: %w", session.ArchiveID, err)
			}
		}
		target := runtimehost.TmuxTarget(
			nodeConfig.TmuxSession, nodeConfig.NodeID, string(session.ID),
		)
		exists, err := driver.TargetExists(ctx, target)
		if err != nil {
			return fmt.Errorf("inspect archived runtime %s: %w", session.Ref().Key(), err)
		}
		if exists {
			if err := driver.Close(ctx, target); err != nil {
				stillExists, inspectErr := driver.TargetExists(ctx, target)
				if inspectErr != nil || stillExists {
					return fmt.Errorf("deactivate archived runtime %s: %w", session.Ref().Key(), err)
				}
			}
		}
		if err := archives.FinalizeArchive(session.ArchiveID); err != nil {
			return fmt.Errorf("finalize archive %s: %w", session.ArchiveID, err)
		}
	}
	return nil
}

func archiveRuntimeRequest(session domain.Session) runtimehost.Request {
	return runtimehost.Request{
		OperationID: "reconcile-" + session.ArchiveID,
		ActorID:     int64(session.OwnerID), NodeID: string(session.NodeID),
		SessionID: string(session.ID), ExpectedGeneration: session.RuntimeGeneration,
		Action: runtimehost.ActionClose, Backend: session.Backend,
		ArchiveCommitID: session.ArchiveID,
		Archive: &runtimehost.ArchivePayload{
			ArchiveID: session.ArchiveID, OwnerID: int64(session.OwnerID), Name: session.Name,
			Workdir: session.Workdir, ProviderSessionID: session.ProviderSessionID,
			CreatedAt: session.CreatedAt, ArchivedAt: session.ArchivedAt,
			Reason: string(session.ArchiveReason),
		},
	}
}

type archiveStateReader interface {
	State() *domain.State
}

func runLocalArchiveReconciler(
	ctx context.Context,
	state archiveStateReader,
	nodeConfig config.Config,
	driver *runtimehost.TmuxDriver,
	archives *localarchive.Writer,
) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconcileLocalArchives(
				ctx, state.State(), nodeConfig, driver, archives,
			); err != nil && ctx.Err() == nil {
				processlog.Failuref(
					processlog.Critical, processlog.FailureConsistency,
					"bria archive reconcile: outcome=failed",
				)
			}
		}
	}
}
