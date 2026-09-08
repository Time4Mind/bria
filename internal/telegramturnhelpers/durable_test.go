package telegramturnhelpers_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bria/internal/promptpreprocess"
	"bria/internal/telegramturnhelpers"
	"bria/internal/turnprocessing"
)

type inputCustody func(context.Context, turnprocessing.SessionInput) (turnprocessing.InputReceipt, error)

func (accept inputCustody) Accept(ctx context.Context, input turnprocessing.SessionInput) (turnprocessing.InputReceipt, error) {
	return accept(ctx, input)
}

func TestDurableCustodyCannotMutateCallerInputAndMustReturnExactIdentity(t *testing.T) {
	for _, mismatch := range []string{"", "session", "message", "sequence"} {
		t.Run(mismatch, func(t *testing.T) {
			payload := []byte("prompt")
			input := turnprocessing.PreparedInput{Text: "prompt", Attachments: []turnprocessing.AttachmentRef{{Reference: "opaque-photo"}}}
			custody := inputCustody(func(_ context.Context, accepted turnprocessing.SessionInput) (turnprocessing.InputReceipt, error) {
				if string(accepted.Payload) != "prompt" || accepted.Attachments[0].Reference != "opaque-photo" {
					t.Fatalf("accepted input = %#v", accepted)
				}
				accepted.Payload[0] = '!'
				accepted.Attachments[0].Reference = "changed"
				receipt := turnprocessing.InputReceipt{SessionID: accepted.SessionID, MessageID: accepted.MessageID, Sequence: 1}
				switch mismatch {
				case "session":
					receipt.SessionID = "other-session"
				case "message":
					receipt.MessageID = "other-message"
				case "sequence":
					receipt.Sequence = 0
				}
				return receipt, nil
			})
			err := telegramturnhelpers.AcceptInput(context.Background(), custody, "session", "telegram-update:1", input, payload)
			if (err != nil) != (mismatch != "") {
				t.Fatalf("mismatch %q: %v", mismatch, err)
			}
			if string(payload) != "prompt" || input.Attachments[0].Reference != "opaque-photo" {
				t.Fatal("custody mutated caller-owned input")
			}
		})
	}
}

func TestPreparedPersistenceKeepsCallerPayloadAndPropagatesFailure(t *testing.T) {
	payload, err := promptpreprocess.Encode("clean", "raw prompt")
	if err != nil {
		t.Fatal(err)
	}
	input := turnprocessing.DurableLeasedInput{SessionID: "session", MessageID: "telegram-update:2", Sequence: 2, Payload: payload}
	callbacks := turnprocessing.DurableInputCallbacks{OnAccepted: func(context.Context, turnprocessing.DurableInputAcceptance) error { return nil }}
	text, enabled, err := telegramturnhelpers.ValidateLeasedInput(input, callbacks)
	if err != nil || !enabled || text != "raw prompt" {
		t.Fatalf("validated envelope = %q, %t, %v", text, enabled, err)
	}
	if err := telegramturnhelpers.PersistPrepared(context.Background(), input, payload, callbacks); err == nil {
		t.Fatal("prepared payload accepted without persistence callback")
	}
	sentinel := errors.New("persistence failed")
	before := string(payload)
	callbacks.OnPrepared = func(_ context.Context, prepared turnprocessing.DurableInputPreparation) error {
		if prepared.SessionID != input.SessionID || prepared.MessageID != input.MessageID || prepared.Sequence != input.Sequence || string(prepared.Payload) != before {
			t.Fatalf("prepared identity/payload = %#v", prepared)
		}
		prepared.Payload[0] = '!'
		return sentinel
	}
	if err := telegramturnhelpers.PersistPrepared(context.Background(), input, payload, callbacks); !errors.Is(err, sentinel) {
		t.Fatalf("persistence failure = %v", err)
	}
	if string(payload) != before {
		t.Fatal("persistence callback mutated source envelope")
	}
}

func TestLeasedAttachmentRequiresOpaqueCustodyReference(t *testing.T) {
	callbacks := turnprocessing.DurableInputCallbacks{OnAccepted: func(context.Context, turnprocessing.DurableInputAcceptance) error { return nil }}
	for _, reference := range []string{"photo-custody", "/tmp/photo", "../photo", `dir\photo`, " photo-custody "} {
		input := turnprocessing.DurableLeasedInput{
			SessionID: "session", MessageID: "telegram-update:3", Sequence: 3, Payload: []byte("photo"),
			Attachments: []turnprocessing.AttachmentRef{{Reference: reference, Size: 8, SHA256: strings.Repeat("a", 64)}},
		}
		_, _, err := telegramturnhelpers.ValidateLeasedInput(input, callbacks)
		if (err == nil) != (reference == "photo-custody") {
			t.Fatalf("reference %q: %v", reference, err)
		}
	}
}
