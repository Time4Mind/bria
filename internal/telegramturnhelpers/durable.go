package telegramturnhelpers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

// ValidateLeasedInput rejects malformed envelopes before invoking any provider.
func ValidateLeasedInput(input turnprocessing.DurableLeasedInput, callbacks turnprocessing.DurableInputCallbacks) (string, bool, error) {
	_, text, enabled, err := promptpreprocess.Decode(input.Payload)
	prepared := turnprocessing.PreparedInput{Text: text, Attachments: append([]turnprocessing.AttachmentRef(nil), input.Attachments...)}
	if input.SessionID == "" || strings.TrimSpace(input.MessageID) == "" || input.Sequence == 0 ||
		!utf8.Valid(input.Payload) || err != nil || ValidatePreparedInput(prepared) != nil {
		return text, enabled, errors.New("durable leased input is invalid")
	}
	if callbacks.OnAccepted == nil {
		return text, enabled, errors.New("durable input acceptance callback is required")
	}
	return text, enabled, nil
}

// ValidateProvider requires exact acceptance and turn-scoped attachment custody.
func ValidateProvider(submitter sessionruntime.Submitter, attachments turnprocessing.AttachmentCustody, input turnprocessing.DurableLeasedInput) error {
	if _, ok := submitter.(sessionruntime.InteractiveSubmitter); !ok {
		if _, structured := submitter.(turnprocessing.PreparedTurnSubmitter); !structured {
			return errors.New("provider does not expose exact durable acceptance")
		}
	}
	if len(input.Attachments) != 0 {
		if _, ok := submitter.(turnprocessing.PreparedTurnSubmitter); !ok {
			return errors.New("provider does not support structured attachments")
		}
		if attachments == nil {
			return errors.New("attachment custody lifecycle is not configured")
		}
	}
	return nil
}

// PersistPrepared commits the processed envelope before provider acceptance.
func PersistPrepared(ctx context.Context, input turnprocessing.DurableLeasedInput, payload []byte, callbacks turnprocessing.DurableInputCallbacks) error {
	if len(payload) == 0 {
		return nil
	}
	if callbacks.OnPrepared == nil {
		return errors.New("durable preprocessing callback is required")
	}
	if err := callbacks.OnPrepared(ctx, turnprocessing.DurableInputPreparation{
		SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
		Payload: append([]byte(nil), payload...),
	}); err != nil {
		return fmt.Errorf("persist durable preprocessing result: %w", err)
	}
	return nil
}

// AcceptInput transfers independent payload/attachment copies and checks the
// returned custody identity before the controller can acknowledge the input.
func AcceptInput(ctx context.Context, custody turnprocessing.DurableInputCustody, sessionID domain.SessionID, messageID string, input turnprocessing.PreparedInput, payload []byte) error {
	receipt, err := custody.Accept(ctx, turnprocessing.SessionInput{
		SessionID:   sessionID,
		MessageID:   messageID,
		Payload:     append([]byte(nil), payload...),
		Attachments: append([]turnprocessing.AttachmentRef(nil), input.Attachments...),
	})
	if err != nil {
		return err
	}
	if receipt.SessionID != sessionID || receipt.MessageID != messageID || receipt.Sequence == 0 {
		return errors.New("durable input custody returned invalid receipt")
	}
	return nil
}
