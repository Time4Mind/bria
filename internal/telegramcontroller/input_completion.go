package telegramcontroller

import (
	"bria/internal/domain"
	"bria/internal/turnadmission"
	"bria/internal/turncompletion"
	"bria/internal/turnprocessing"
	"context"
	"errors"
)

func (w *sessionWorker) currentCompletion() (*turncompletion.Signal, *turnadmission.Ticket) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.activeCancel == nil || w.admission == nil {
		return nil, nil
	}
	ticket, ok := w.admission.RegisterSteer()
	if !ok {
		return nil, nil
	}
	return w.completion, ticket
}
func (w *sessionWorker) sameCompletion(signal *turncompletion.Signal) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.completion == signal
}

func (w *sessionWorker) waitFinalization(ctx context.Context) error {
	w.mu.Lock()
	signal, finishing := w.completion, w.activeCancel == nil
	admission := w.admission
	w.mu.Unlock()
	if !finishing || signal == nil {
		return nil
	}
	state, err := signal.Wait(ctx)
	if err != nil {
		return err
	}
	if admission != nil {
		err = admission.Wait(ctx)
	}
	if err != nil && !errors.Is(err, turnadmission.ErrCommitFailed) {
		return err
	}
	if err != nil || state != string(DurableInputSucceeded) && state != string(DurableInputTerminalFailed) {
		// Recovery may commit before cleanup settles. Never drop the signal;
		// only a fresh exact higher-generation Ready supersedes its outcome.
		current, loadErr := w.controller.sessions.Load(ctx, w.sessionID)
		w.controller.mu.Lock()
		_, live := w.controller.usableLocked(current)
		w.controller.mu.Unlock()
		binding, bound := current.Binding()
		w.mu.Lock()
		prior, same := w.completionBinding, w.completion == signal
		w.mu.Unlock()
		if loadErr != nil || !live || !bound || !same || current.Status() != domain.SessionReady ||
			prior.SessionID == "" || binding.Provider != prior.Provider || binding.SessionID != prior.SessionID || binding.Generation <= prior.Generation {
			return turnprocessing.ErrInputDeferred
		}
	}
	return nil
}

func (c *Controller) completeInput(callbacks DurableInputCallbacks, input DurableLeasedInput, outcome DurableInputCompletion) error {
	if callbacks.OnCompleted == nil {
		return nil
	}
	receipt := DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Accepted: true, Completion: outcome}
	if err := callbacks.OnCompleted(context.WithoutCancel(c.rootContext), receipt); err != nil {
		c.notify(context.WithoutCancel(c.rootContext), Notification{OperationID: input.MessageID + ":completion-journal-error", ConversationID: c.ownerPrivateChatID, SessionID: input.SessionID, Kind: NotificationError, Text: "Не удалось подтвердить сохранение исхода запроса. Повторная отправка не выполнялась."})
		return err
	}
	return nil
}

func (c *Controller) awaitInputCompletion(input DurableLeasedInput, callbacks DurableInputCallbacks, terminal *turncompletion.Signal, ticket *turnadmission.Ticket) {
	// ProcessDurableInput already holds durableWork while adding this child.
	c.durableWork.Add(1)
	go func() {
		defer c.durableWork.Done()
		// Shutdown cancels the provider, not its terminal proof. The tracked
		// producer resolves this signal after its bounded cancellation drain.
		state, err := terminal.Wait(context.WithoutCancel(c.rootContext))
		outcome := DurableInputCompletion(state)
		if err != nil || state == "" {
			outcome = DurableInputAwaitingRecovery
		}
		ticket.Finish(c.completeInput(callbacks, input, outcome))
	}()
}
