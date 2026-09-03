package turnprocessing_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

func TestExecuteOwnsExactAcceptanceInteractionAndAttachmentOrder(t *testing.T) {
	var events []string
	request := turnprocessing.Request{
		SessionID: "logical-1", ProviderSessionID: "provider-1", MessageID: "telegram-update:1",
		Input: turnprocessing.PreparedInput{Text: "look", Attachments: []turnprocessing.AttachmentRef{{Reference: "photo-1", Size: 3, SHA256: "hash"}}},
	}
	custody := &attachmentCustody{events: &events}
	interactions := &interactionHandler{events: &events}
	submitter := &preparedSubmitter{run: func(ctx context.Context, id domain.SessionID, input turnprocessing.PreparedInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if id != request.SessionID || !reflect.DeepEqual(input, request.Input) {
			t.Fatalf("provider input = (%q, %#v)", id, input)
		}
		if err := callbacks.OnAccepted(request.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		if err := callbacks.OnEvent(sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "working"}); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		interaction := sessionruntime.InteractionRequest{ID: "question-1", Kind: "question", ItemID: "item-1", Blocking: true}
		if _, err := callbacks.OnInteraction(ctx, interaction); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		if err := callbacks.OnInteractionResponseAccepted(sessionruntime.InteractionResponseAcceptance{
			ProviderSessionID: request.ProviderSessionID, MessageID: request.MessageID, InteractionID: interaction.ID,
		}); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
	}}
	execution, err := turnprocessing.Execute(context.Background(), submitter, interactions, custody, request, turnprocessing.Callbacks{
		MarkInputAccepted: func(context.Context) error { events = append(events, "input-accepted"); return nil },
		AfterAccepted:     func() { events = append(events, "after-accepted") },
		OnEvent:           func(sessionruntime.TurnEvent) error { events = append(events, "event"); return nil },
	})
	if err != nil || !execution.Accepted || !execution.StreamedEvents || execution.Result.Final != "done" {
		t.Fatalf("Execute() = (%#v, %v)", execution, err)
	}
	if err := turnprocessing.CompleteAttachments(context.Background(), custody, request); err != nil {
		t.Fatal(err)
	}
	want := []string{"input-accepted", "attachment-accepted", "after-accepted", "event", "interaction", "interaction-confirmed", "attachment-completed"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestExecuteRejectsMismatchedProviderAcceptanceBeforeCustody(t *testing.T) {
	submitter := &preparedSubmitter{run: func(_ context.Context, _ domain.SessionID, _ turnprocessing.PreparedInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		return sessionruntime.TurnResult{}, callbacks.OnAccepted("different-message")
	}}
	custody := &attachmentCustody{}
	request := turnprocessing.Request{
		SessionID: "logical-1", ProviderSessionID: "provider-1", MessageID: "telegram-update:1",
		Input: turnprocessing.PreparedInput{Attachments: []turnprocessing.AttachmentRef{{Reference: "photo-1"}}},
	}
	execution, err := turnprocessing.Execute(context.Background(), submitter, nil, custody, request, turnprocessing.Callbacks{
		MarkInputAccepted: func(context.Context) error { t.Fatal("mismatched input reached custody"); return nil },
	})
	if err == nil || execution.Accepted || custody.accepted != 0 {
		t.Fatalf("Execute() = (%#v, %v), custody accepted=%d", execution, err, custody.accepted)
	}
}

type preparedSubmitter struct {
	run func(context.Context, domain.SessionID, turnprocessing.PreparedInput, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error)
}

func (*preparedSubmitter) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("plain submit must not be used")
}

func (submitter *preparedSubmitter) SubmitPreparedWithCallbacks(ctx context.Context, id domain.SessionID, input turnprocessing.PreparedInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return submitter.run(ctx, id, input, callbacks)
}

type attachmentCustody struct {
	events   *[]string
	accepted int
}

func (custody *attachmentCustody) MarkAccepted(context.Context, turnprocessing.AttachmentReceipt) error {
	custody.accepted++
	if custody.events != nil {
		*custody.events = append(*custody.events, "attachment-accepted")
	}
	return nil
}

func (custody *attachmentCustody) MarkCompleted(context.Context, turnprocessing.AttachmentReceipt) error {
	if custody.events != nil {
		*custody.events = append(*custody.events, "attachment-completed")
	}
	return nil
}

type interactionHandler struct{ events *[]string }

func (handler *interactionHandler) ResolveInteraction(_ context.Context, request turnprocessing.InteractionEnvelope) (sessionruntime.InteractionResponse, error) {
	*handler.events = append(*handler.events, "interaction")
	return sessionruntime.InteractionResponse{ID: request.Request.ID, Outcome: "answered"}, nil
}

func (handler *interactionHandler) ConfirmInteractionResponse(_ context.Context, _ turnprocessing.InteractionResponseAcceptance) error {
	*handler.events = append(*handler.events, "interaction-confirmed")
	return nil
}
