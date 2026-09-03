package interactionflow

import (
	"context"
	"errors"

	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

var (
	ErrPendingTextUnavailable = errors.New("provider interaction has no exact pending text request")
	ErrSecretDeletionUnknown  = errors.New("provider interaction secret deletion is unknown")
)

// ConsumeBoundSourceMessage performs only an exact durable tombstone lookup.
// It never selects a live pending request and is safe to call before auth and
// ordinary command/input routing after Telegram offset replay.
func (flow *Flow) ConsumeBoundSourceMessage(ctx context.Context, input telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error) {
	if flow == nil || ctx == nil || input.ActorID <= 0 || input.ConversationID <= 0 || input.SourceMessageID <= 0 {
		return telegramcontroller.InteractionTextResult{}, ErrInvalidEnvelope
	}
	source, found, err := flow.store.LoadConsumedSource(ctx, input.ActorID, input.ConversationID, input.SourceMessageID)
	if err != nil {
		return telegramcontroller.InteractionTextResult{}, err
	}
	if !found {
		return telegramcontroller.InteractionTextResult{}, nil
	}
	result := telegramcontroller.InteractionTextResult{
		Handled: true, Status: "Секретный ответ уже принят.", Secret: true, DeletionKnown: source.DeletionKnown,
	}
	if !source.DeletionKnown {
		if flow.ownerActorID <= 0 || input.ActorID != flow.ownerActorID || input.ConversationID != flow.conversationID ||
			input.ConversationKind != "private" || flow.secretDeleter == nil {
			return result, ErrInvalidEnvelope
		}
		// A redelivery of the exact bound source is the retry trigger. Deleters must
		// treat an already-absent exact message as success; arbitrary prompts are
		// never inspected and a different source cannot trigger this mutation.
		if err := flow.secretDeleter.DeleteMessage(context.WithoutCancel(ctx), source.ConversationID, source.MessageID); err != nil {
			return result, ErrSecretDeletionUnknown
		}
		source.UpdatedAt = flow.now().UTC()
		confirmed, changed, err := flow.store.ConfirmConsumedSourceDeletion(context.WithoutCancel(ctx), source, source.Revision)
		if err != nil {
			return result, err
		}
		if !changed || !confirmed.DeletionKnown {
			return result, ErrSecretDeletionUnknown
		}
		source = confirmed
	}
	result.DeletionKnown = true

	// If the original provider interaction is still locally waiting behind the
	// deletion fence, the redelivered update carries the only surviving copy of
	// the secret. Finish that exact operation once; a provider-unknown or pruned
	// operation is never replayed.
	operation, found, err := flow.store.Load(context.WithoutCancel(ctx), source.OperationID)
	if err != nil || !found || operation.Phase != PhaseSecretDeletionUnknown || operation.SecretSourceMessageID != source.MessageID {
		return result, err
	}
	if err := flow.finishSecretResponse(context.WithoutCancel(ctx), operation, input.Text); err != nil {
		return result, err
	}
	return result, nil
}

// ResolvePendingText implements the controller's authenticated pre-input port.
// Selection uses the one durable waiting_text operation, never prompt text.
func (flow *Flow) ResolvePendingText(ctx context.Context, input telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error) {
	if flow == nil || ctx == nil || input.ConversationID <= 0 || input.SourceMessageID <= 0 {
		return telegramcontroller.InteractionTextResult{}, ErrInvalidEnvelope
	}
	operation, found, err := flow.store.PendingText(ctx, input.ConversationID)
	if err != nil {
		return telegramcontroller.InteractionTextResult{}, err
	}
	if !found {
		return telegramcontroller.InteractionTextResult{}, nil
	}
	question := operation.Request.Questions[operation.QuestionIndex]
	result := telegramcontroller.InteractionTextResult{
		Handled: true, Status: "Ответ принят.", Secret: question.IsSecret, DeletionKnown: !question.IsSecret,
	}
	if flow.ownerActorID <= 0 || input.ActorID != flow.ownerActorID || input.ConversationID != flow.conversationID || input.ConversationKind != "private" {
		return result, ErrInvalidEnvelope
	}
	if operation.Phase == PhaseSecretDeletionUnknown {
		if input.SourceMessageID != operation.SecretSourceMessageID {
			return result, ErrSecretDeletionUnknown
		}
		// Repair the only possible split-write crash gap from the content-free
		// coordinates already fenced in the operation. No secret text is persisted.
		if _, found, err := flow.store.LoadConsumedSource(ctx, input.ActorID, input.ConversationID, input.SourceMessageID); err != nil {
			return result, err
		} else if !found {
			if _, err := flow.store.RecordConsumedSource(context.WithoutCancel(ctx), ConsumedSource{
				OperationID: operation.ID, ActorID: input.ActorID, ConversationID: input.ConversationID,
				MessageID: input.SourceMessageID, CreatedAt: operation.UpdatedAt, UpdatedAt: operation.UpdatedAt,
			}); err != nil {
				return result, err
			}
		}
		return flow.ConsumeBoundSourceMessage(ctx, input)
	}
	if input.MediaKind != "" || input.Caption != "" || input.Text == "" {
		result.Status = "Нужен текстовый ответ."
		return result, nil
	}
	answers := cloneAnswers(operation.Answers)
	answers[question.ID] = []string{input.Text}
	response := sessionruntime.InteractionResponse{
		ID: operation.ProviderRequestID, Outcome: runtimeprotocol.OutcomeAnswered, Answers: answers,
	}
	if runtimeprotocol.ValidateResponse(operation.Request, response, runtimeprotocol.Limits{}) != nil {
		result.Status = "Ответ не принят: текст недопустим."
		return result, nil
	}
	if !question.IsSecret {
		next := cloneOperation(operation)
		next.Answers = answers
		next.QuestionIndex++
		next.Response = &response
		next.Phase = PhaseResponseReady
		next.Resolution = "other_answered"
		next.UpdatedAt = flow.now().UTC()
		updated, changed, err := flow.store.CompareAndSwap(ctx, operation.ID, operation.Revision, next)
		if err != nil {
			return result, err
		}
		if !changed {
			return result, ErrPendingTextUnavailable
		}
		flow.signal(updated.ID)
		return result, nil
	}
	if flow.secretDeleter == nil {
		return result, ErrInvalidConfiguration
	}

	// Fence before the external delete. Reopen never repeats an ambiguous
	// deletion and the source text itself is not copied into durable state.
	flow.setSecretInFlight(operation.ID, true)
	fenced := cloneOperation(operation)
	fenced.Phase = PhaseSecretDeletionUnknown
	fenced.SecretSourceMessageID = input.SourceMessageID
	fenced.Resolution = "secret_deletion_unknown"
	fenced.UpdatedAt = flow.now().UTC()
	fenced, changed, err := flow.store.CompareAndSwap(ctx, operation.ID, operation.Revision, fenced)
	if err != nil || !changed {
		flow.setSecretInFlight(operation.ID, false)
		if err != nil {
			return result, err
		}
		return result, ErrPendingTextUnavailable
	}
	source, err := flow.store.RecordConsumedSource(context.WithoutCancel(ctx), ConsumedSource{
		OperationID: operation.ID, ActorID: input.ActorID, ConversationID: input.ConversationID,
		MessageID: input.SourceMessageID, CreatedAt: flow.now().UTC(), UpdatedAt: flow.now().UTC(),
	})
	if err != nil {
		flow.setSecretInFlight(operation.ID, false)
		flow.signal(operation.ID)
		return result, err
	}
	if err := flow.secretDeleter.DeleteMessage(context.WithoutCancel(ctx), input.ConversationID, input.SourceMessageID); err != nil {
		// Keep the live provider request waiting: an exact Telegram redelivery can
		// retry this bound deletion and complete the one-shot response.
		return result, ErrSecretDeletionUnknown
	}
	source.UpdatedAt = flow.now().UTC()
	if _, changed, err := flow.store.ConfirmConsumedSourceDeletion(context.WithoutCancel(ctx), source, source.Revision); err != nil || !changed {
		flow.setSecretInFlight(operation.ID, false)
		flow.signal(operation.ID)
		if err != nil {
			return result, err
		}
		return result, ErrSecretDeletionUnknown
	}

	if err := flow.finishSecretResponse(context.WithoutCancel(ctx), fenced, input.Text); err != nil {
		return result, err
	}
	result.DeletionKnown = true
	return result, nil
}

func (flow *Flow) finishSecretResponse(ctx context.Context, operation Operation, answer string) error {
	question := operation.Request.Questions[operation.QuestionIndex]
	answers := cloneAnswers(operation.Answers)
	answers[question.ID] = []string{answer}
	response := sessionruntime.InteractionResponse{
		ID: operation.ProviderRequestID, Outcome: runtimeprotocol.OutcomeAnswered, Answers: answers,
	}
	if runtimeprotocol.ValidateResponse(operation.Request, response, runtimeprotocol.Limits{}) != nil {
		return ErrInvalidEnvelope
	}
	flow.putSecretResponse(operation.ID, response)
	next := cloneOperation(operation)
	next.Phase = PhaseProviderResponseUnknown
	next.SecretResponse = true
	next.Resolution = "secret_answered"
	next.UpdatedAt = flow.now().UTC()
	_, changed, err := flow.store.CompareAndSwap(ctx, operation.ID, operation.Revision, next)
	if err != nil || !changed {
		flow.dropSecretResponse(operation.ID)
		flow.setSecretInFlight(operation.ID, false)
		flow.signal(operation.ID)
		if err != nil {
			return err
		}
		return ErrProviderResponseUnknown
	}
	flow.setSecretInFlight(operation.ID, false)
	flow.signal(operation.ID)
	return nil
}
