package telegramcontroller

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontrolport"
	"bria/internal/turnadmission"
	"bria/internal/turncontinuation"
	"bria/internal/turnprocessing"
)

type AcceptedTurnObserver = telegramcontrolport.AcceptedTurnObserver

func (c *Controller) observer() AcceptedTurnObserver {
	if c.acceptedObserver != nil {
		return c.acceptedObserver
	}
	observer, _ := c.submitter.(AcceptedTurnObserver)
	return observer
}
func (c *Controller) ContinueAcceptedInput(ctx context.Context, binding domain.ProviderBinding, input DurableLeasedInput, callbacks DurableInputCallbacks) error {
	return c.ContinueAcceptedBatch(ctx, binding, []turncontinuation.Member{{Input: input, Callbacks: callbacks, Accepted: true}}, nil)
}

// ContinueAcceptedBatch validates the entire custody plan before any observer starts.
func (c *Controller) ContinueAcceptedBatch(ctx context.Context, binding domain.ProviderBinding, members []turncontinuation.Member, settled func()) error {
	return c.continueAcceptedBatch(ctx, binding, members, nil, settled, nil)
}

// ContinueAcceptedBatchWithRecovery preserves the same observation pipeline,
// adding one fallible durable recovery commit before lifecycle Ready.
func (c *Controller) ContinueAcceptedBatchWithRecovery(ctx context.Context, binding domain.ProviderBinding, members []turncontinuation.Member, finalizeRecovery func(context.Context) error, settled func()) error {
	if finalizeRecovery == nil {
		return errors.New("accepted recovery finalizer is required")
	}
	done := make(chan error, 1)
	if err := c.continueAcceptedBatch(ctx, binding, members, finalizeRecovery, settled, func(err error) { done <- err }); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) continueAcceptedBatch(ctx context.Context, binding domain.ProviderBinding, members []turncontinuation.Member, finalizeRecovery func(context.Context) error, settled func(), completed func(error)) error {
	groups, err := turncontinuation.Plan(members)
	if err != nil {
		return err
	}
	if ctx == nil || c.observer() == nil {
		return errors.New("accepted observation context or capability is missing")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := groups[0].Members[0].Input.SessionID
	current, err := c.sessions.Load(ctx, id)
	if err != nil {
		return err
	}
	actual, bound := current.Binding()
	if current.ID() != id || !bound || binding != actual {
		return sessionruntime.ErrBindingMismatch
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("Telegram controller is closed")
	}
	worker := c.workers[id]
	var prior *turncontinuation.Run
	active := false
	if worker != nil {
		worker.mu.Lock()
		prior = worker.continuation
		active = worker.activeCancel != nil
		defer worker.mu.Unlock()
	}
	run, start, err := turncontinuation.Claim(prior, binding, groups, active)
	if err != nil || !start {
		return err
	}
	if current.Status() != domain.SessionRunning && current.Status() != domain.SessionStopping && current.Status() != domain.SessionClosingAfterWork {
		return errors.New("accepted continuation requires an unresolved running turn")
	}
	c.live[id] = current
	if worker == nil {
		worker = c.ensureWorkerLocked(id)
		worker.mu.Lock()
		defer worker.mu.Unlock()
	}
	worker.continuation = run
	admission := turnadmission.NewAdmission()
	worker.admission = admission
	runContext := c.rootContext
	if completed != nil {
		runContext = ctx
	}
	c.durableWork.Add(1)
	go func() {
		defer c.durableWork.Done()
		var observedRoot string
		err := turncontinuation.Execute(runContext, groups, func(m turncontinuation.Member) DurableInputCompletion {
			observedRoot = m.Input.MessageID
			current, err := c.sessions.Load(runContext, id)
			if actual, bound := current.Binding(); err != nil || !bound || actual != binding {
				return DurableInputAwaitingRecovery
			}
			outcome, _ := worker.runTurnWithAcceptance(runContext, queuedTurn{observedBinding: &binding, messageID: m.Input.MessageID, attachments: m.Input.Attachments, admission: admission}, func(context.Context) error { return nil })
			return outcome
		}, func(m turncontinuation.Member, outcome DurableInputCompletion) error {
			if outcome != DurableInputSucceeded && outcome != DurableInputTerminalFailed {
				return c.completeInput(m.Callbacks, m.Input, outcome)
			}
			request := turnprocessing.Request{SessionID: id, ProviderSessionID: binding.SessionID, MessageID: m.Input.MessageID, Input: PreparedInput{Attachments: m.Input.Attachments}}
			attachmentsDone := m.Input.MessageID == observedRoot
			return c.retryFinalization(runContext, id, binding, m.Input.MessageID, func(attempt context.Context) error {
				if !attachmentsDone {
					if err := turnprocessing.CompleteAttachments(attempt, c.attachments, request); err != nil {
						return err
					}
					attachmentsDone = true
				}
				return m.Callbacks.OnCompleted(attempt, DurableInputProcessReceipt{SessionID: id, MessageID: m.Input.MessageID, Sequence: m.Input.Sequence, Accepted: true, Completion: outcome})
			})
		}, func() error {
			message := groups[len(groups)-1].Members[0].Input.MessageID
			if finalizeRecovery != nil {
				if err := c.retryFinalization(runContext, id, binding, message+":recovery-skip", finalizeRecovery); err != nil {
					return err
				}
			}
			current, err := c.sessions.Load(runContext, id)
			if actual, bound := current.Binding(); err != nil || !bound || actual != binding {
				return sessionruntime.ErrBindingMismatch
			}
			_, err = worker.finishLifecycle(runContext, message, binding)
			return err
		})
		admission.Seal()
		worker.mu.Lock()
		run.Done = true
		worker.mu.Unlock()
		admission.FinishRoot(err)
		if err == nil && settled != nil {
			settled()
		}
		if completed != nil {
			completed(err)
		}
	}()
	return nil
}
func (w *sessionWorker) executeRequest(ctx context.Context, turn queuedTurn, request turnprocessing.Request, callbacks turnprocessing.Callbacks) (turnprocessing.Execution, error) {
	if turn.observedBinding == nil {
		return turnprocessing.Execute(ctx, w.controller.submitter, w.controller.interactions, w.controller.attachments, request, callbacks)
	}
	return turncontinuation.Observe(ctx, w.controller.observer(), *turn.observedBinding, request, callbacks)
}
