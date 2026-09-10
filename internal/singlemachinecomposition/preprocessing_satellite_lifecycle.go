package singlemachinecomposition

import (
	"context"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/promptpreprocesssession"
	"bria/internal/telegramcontroller"
)

const satelliteLifecycleCleanupTimeout = 10 * time.Second

type preprocessingSatelliteLifecycle interface {
	Activate(context.Context, domain.SessionID) error
	Archive(context.Context, domain.SessionID) error
	Restore(context.Context, domain.SessionID) error
	Forget(context.Context, domain.SessionID) error
	Reconcile(context.Context, []promptpreprocesssession.PrimaryState) error
}

func preprocessingSatelliteStates(local domain.ComputerID, sessions []domain.Session) []promptpreprocesssession.PrimaryState {
	states := make([]promptpreprocesssession.PrimaryState, 0, len(sessions))
	for _, session := range sessions {
		if session.ComputerID() != local {
			continue
		}
		desired := promptpreprocesssession.DesiredActive
		if session.Status() == domain.SessionArchived {
			desired = promptpreprocesssession.DesiredArchived
		}
		states = append(states, promptpreprocesssession.PrimaryState{SessionID: session.ID(), Desired: desired})
	}
	return states
}

type preprocessingSatelliteLifecycleReporter func(context.Context, string, domain.SessionID, error)

func applySatelliteCreation(
	ctx context.Context,
	satellites preprocessingSatelliteLifecycle,
	report preprocessingSatelliteLifecycleReporter,
	result app.CreateSessionResult,
	err error,
) {
	if err != nil || result.StartError != nil || result.Session.Status() != domain.SessionReady || satellites == nil {
		return
	}
	reportSatelliteLifecycle(ctx, report, "activate", result.Session.ID(), satellites.Activate(context.WithoutCancel(ctx), result.Session.ID()))
}

type satelliteArchivedResumer struct {
	base       telegramcontroller.ArchivedResumer
	satellites preprocessingSatelliteLifecycle
	report     preprocessingSatelliteLifecycleReporter
}

func (resumer satelliteArchivedResumer) Resume(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	resumed, err := resumer.base.Resume(ctx, id)
	if err == nil && resumed.Status() == domain.SessionReady && resumer.satellites != nil {
		reportSatelliteLifecycle(ctx, resumer.report, "restore", id, resumer.satellites.Restore(context.WithoutCancel(ctx), id))
	}
	return resumed, err
}

type sessionCloseWorkflow interface {
	Close(context.Context, domain.SessionID) (app.CloseSessionResult, error)
	BeginCloseWithCompletion(context.Context, domain.SessionID, func(context.Context, app.CloseSessionResult, error)) (app.CloseSessionResult, error)
}

type satelliteSessionCloser struct {
	base       sessionCloseWorkflow
	satellites preprocessingSatelliteLifecycle
	report     preprocessingSatelliteLifecycleReporter
}

func (closer satelliteSessionCloser) Close(ctx context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
	result, err := closer.base.Close(ctx, id)
	closer.apply(ctx, result, err)
	return result, err
}

func (closer satelliteSessionCloser) BeginClose(ctx context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
	return closer.BeginCloseWithCompletion(ctx, id, nil)
}

func (closer satelliteSessionCloser) BeginCloseWithCompletion(ctx context.Context, id domain.SessionID, completed func(context.Context, app.CloseSessionResult, error)) (app.CloseSessionResult, error) {
	result, err := closer.base.BeginCloseWithCompletion(ctx, id, func(completionContext context.Context, completedResult app.CloseSessionResult, completionErr error) {
		if completed != nil {
			completed(completionContext, completedResult, completionErr)
		}
		closer.apply(completionContext, completedResult, completionErr)
	})
	if err != nil || !result.Scheduled {
		closer.apply(ctx, result, err)
	}
	return result, err
}

func (closer satelliteSessionCloser) apply(ctx context.Context, result app.CloseSessionResult, err error) {
	if err != nil || closer.satellites == nil || result.Session.ID() == "" {
		return
	}
	action := ""
	var lifecycleErr error
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), satelliteLifecycleCleanupTimeout)
	defer cancel()
	if result.Deleted {
		action = "forget"
		lifecycleErr = closer.satellites.Forget(cleanupContext, result.Session.ID())
	} else if result.Session.Status() == domain.SessionArchived {
		action = "archive"
		lifecycleErr = closer.satellites.Archive(cleanupContext, result.Session.ID())
	}
	if action != "" {
		reportSatelliteLifecycle(ctx, closer.report, action, result.Session.ID(), lifecycleErr)
	}
}

func reportSatelliteLifecycle(ctx context.Context, report preprocessingSatelliteLifecycleReporter, action string, id domain.SessionID, err error) {
	if report != nil {
		report(context.WithoutCancel(ctx), action, id, err)
	}
}
