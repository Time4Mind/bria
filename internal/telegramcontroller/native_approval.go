package telegramcontroller

import (
	"context"
	"errors"

	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/nativeapprovalflow"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
)

type nativeApprovalObserver struct{ observer controllertelemetry.Observer }

func (o nativeApprovalObserver) ObserveNativeApproval(ctx context.Context, event nativeapprovalflow.Event) {
	if o.observer == nil {
		return
	}
	outcome := controllertelemetry.ApprovalConfirmed
	if event.Outcome == nativeapprovalflow.Stale {
		outcome = controllertelemetry.ApprovalStale
	} else if event.Outcome == nativeapprovalflow.Uncertain {
		outcome = controllertelemetry.ApprovalUncertain
	}
	o.observer.ObserveControllerEvent(ctx, controllertelemetry.Event{
		Stage: controllertelemetry.NativeApproval, Outcome: outcome,
		SessionID: event.SessionID, ProviderSessionID: event.ProviderSessionID,
		ApprovalFingerprint: event.Fingerprint, Generation: event.Generation,
	})
}

func (c *Controller) autoApprovalEnabled(ctx context.Context) (bool, error) {
	if c.settings == nil {
		return false, nil
	}
	snapshot, err := c.settings.Snapshot(ctx)
	return snapshot.AutoApproveCommands, err
}

func (c *Controller) toggleNativeAutoApprovals(ctx context.Context) error {
	p, ok := c.settings.(settingsport.AutoApprovalPreferences)
	if !ok {
		return errors.New("automatic approval settings are not configured")
	}
	return c.nativeApprovals.Toggle(ctx, p.ToggleAutoApproveCommands)
}

// Automatic decisions do not depend on which card is currently visible.
// Only known Codex sessions and exact live native snapshots are considered.
func (c *Controller) autoApproveNativeScreens(ctx context.Context, reader sessionruntime.NativeScreenProvider) {
	if c.sessions == nil || c.native == nil || c.settings == nil {
		return
	}
	sessions, err := c.sessions.List(ctx)
	if err != nil {
		return
	}
	for _, session := range sessions {
		if session.Provider() != domain.ProviderCodex {
			continue
		}
		// An uncertain decision is retained by the flow and never resent.
		_ = c.nativeApprovals.Observe(ctx, session.ID(), reader, c.native, c.autoApprovalEnabled)
	}
}
