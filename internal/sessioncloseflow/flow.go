// Package sessioncloseflow coordinates interactive close completion and emits
// typed, payload-free evidence for lifecycle and node selection boundaries.
package sessioncloseflow

import (
	"context"
	"sync"
	"time"

	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/telegramnodes"
)

type CompletingCloser interface {
	BeginCloseWithCompletion(context.Context, domain.SessionID, func(context.Context, app.CloseSessionResult, error)) (app.CloseSessionResult, error)
}
type CloseFunc func(context.Context, domain.SessionID) (app.CloseSessionResult, error)
type Flow struct {
	Observer controllertelemetry.Observer
	pending  sync.Map
}
type pendingClose struct{ operation string }

// Close binds a busy close before its durable transition can race turn finish.
// Keep the first accepted close operation; synchronous/failed closes need no lease.
func (f *Flow) Close(ctx context.Context, id domain.SessionID, closeFn CloseFunc) (app.CloseSessionResult, error) {
	lease := &pendingClose{operation: controllertelemetry.Operation(ctx)}
	owned := false
	if lease.operation != "" {
		_, loaded := f.pending.LoadOrStore(id, lease)
		owned = !loaded
	}
	result, err := closeFn(ctx, id)
	if owned && (err != nil || result.Session.ID() != id || !result.Scheduled || result.Session.Status() != domain.SessionClosingAfterWork) {
		f.pending.CompareAndDelete(id, lease)
	}
	return result, err
}

// CompletionContext consumes correlation only; the durable lifecycle authorizes
// close-after-work. After restart no initiating operation is invented.
func (f *Flow) CompletionContext(ctx context.Context, id domain.SessionID) context.Context {
	if operation, ok := f.pending.LoadAndDelete(id); ok {
		return controllertelemetry.WithOperation(ctx, operation.(*pendingClose).operation)
	}
	return ctx
}

// Begin gates asynchronous completion behind handling the initial durable
// result. The closer callback runs outside its lifecycle lock and may block until
// BeginCloseWithCompletion returns; cancellation does not discard its evidence.
func (f *Flow) Begin(ctx context.Context, id domain.SessionID, closer CompletingCloser, initial func(CloseFunc) error, completed func(context.Context, app.CloseSessionResult) error) error {
	initialHandled := make(chan struct{})
	defer close(initialHandled)
	return initial(func(ctx context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
		return closer.BeginCloseWithCompletion(ctx, id, func(ctx context.Context, result app.CloseSessionResult, err error) {
			<-initialHandled
			f.Outcome(ctx, id, result, err, controllertelemetry.ScheduledClose)
			if err != nil || result.Session.ID() != id || result.Scheduled || (!result.Deleted && result.Session.Status() != domain.SessionArchived) {
				return
			}
			_ = completed(context.WithoutCancel(ctx), result)
		})
	})
}

func (f *Flow) Observe(ctx context.Context, event controllertelemetry.Event) {
	if f.Observer != nil {
		event.Time, event.OperationID = time.Now().UTC(), controllertelemetry.Operation(ctx)
		f.Observer.ObserveControllerEvent(ctx, event)
	}
}

func (f *Flow) Selection(ctx context.Context, session domain.Session, nodes *telegramnodes.Scope, selectFn func() error) error {
	err := selectFn()
	e := controllertelemetry.Event{Stage: controllertelemetry.SelectionPersist, Reason: controllertelemetry.ManualSelection, Outcome: controllertelemetry.Persisted, SessionID: string(session.ID()), NodeID: string(session.ComputerID()), TargetSessionID: string(session.ID())}
	if !nodes.PersistsSelection() {
		e.Outcome, e.Reason = controllertelemetry.Skipped, controllertelemetry.StoreUnavailable
	}
	if err != nil {
		e.Outcome, e.Reason = controllertelemetry.Failed, controllertelemetry.PersistFailed
	}
	f.Observe(ctx, e)
	return err
}

func (f *Flow) Outcome(ctx context.Context, id domain.SessionID, result app.CloseSessionResult, err error, reason controllertelemetry.Reason) {
	e := controllertelemetry.Event{Stage: controllertelemetry.ArchiveOutcome, SessionID: string(id), NodeID: string(result.Session.ComputerID()), Reason: reason, Outcome: controllertelemetry.Archived}
	switch {
	case err != nil:
		e.Outcome, e.Reason = controllertelemetry.Failed, controllertelemetry.CloseFailed
	case result.Session.ID() != id:
		e.Outcome, e.Reason = controllertelemetry.Failed, controllertelemetry.InvalidCloseResult
	case result.Scheduled && (result.Session.Status() == domain.SessionClosing || result.Session.Status() == domain.SessionClosingAfterWork):
		e.Outcome, e.Reason = controllertelemetry.Scheduled, controllertelemetry.ScheduledClose
	case result.Deleted:
		e.Outcome = controllertelemetry.Deleted
	case result.Scheduled || result.Session.Status() != domain.SessionArchived:
		e.Outcome, e.Reason = controllertelemetry.Failed, controllertelemetry.InvalidCloseResult
	}
	f.Observe(ctx, e)
}

// RemoveClosed reports exactly the persistence capability/result returned by
// the node scope. Callers own serialization with user navigation and foreground UI.
func (f *Flow) RemoveClosed(ctx context.Context, nodes *telegramnodes.Scope, session domain.Session) (telegramnodes.ClosedSelection, error) {
	r, err := nodes.RemoveClosed(ctx, session)
	e := controllertelemetry.Event{Stage: controllertelemetry.FallbackChoice, SessionID: string(session.ID()), NodeID: string(session.ComputerID()), PreviousSessionID: string(r.Previous), TargetSessionID: string(r.Active), CandidatesKnown: r.CandidatesKnown, CandidateCount: r.CandidateCount, Outcome: controllertelemetry.Selected, Reason: controllertelemetry.DurableSelectable}
	switch {
	case !r.CandidatesKnown:
		e.Outcome, e.Reason = controllertelemetry.Failed, controllertelemetry.ListFailed
	case r.Preserved:
		e.Outcome, e.Reason = controllertelemetry.Preserved, controllertelemetry.NewerSelection
	case r.Active == "":
		e.Outcome, e.Reason = controllertelemetry.Cleared, controllertelemetry.NoSelectable
	case r.Recent:
		e.Reason = controllertelemetry.RecentSelectable
	}
	if r.CandidatesKnown && r.Selected != session.ComputerID() {
		e.Reason = controllertelemetry.OtherNode
	}
	f.Observe(ctx, e)
	if r.CandidatesKnown {
		e.Stage, e.Outcome = controllertelemetry.SelectionPersist, controllertelemetry.Persisted
		if !r.Persisted {
			e.Outcome, e.Reason = controllertelemetry.Skipped, controllertelemetry.StoreUnavailable
		}
		if err != nil {
			e.Outcome, e.Reason = controllertelemetry.Failed, controllertelemetry.PersistFailed
		}
		f.Observe(ctx, e)
	}
	return r, err
}
