// Package inputcomposition connects production media preparation to the
// controller's structured, durable attachment boundary.
package inputcomposition

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"bria/internal/mediaproduction"
	"bria/internal/turnprocessing"
)

var (
	ErrInvalidRuntime       = errors.New("input composition runtime is invalid")
	ErrUnsafeAttachment     = errors.New("input composition attachment is unsafe")
	ErrStructuredAttachment = errors.New("input composition cannot flatten an attachment into prompt text")
)

// Composition is both the controller's structured preparer and its exact
// attachment-custody lifecycle. The opaque reference never enters prompt text.
type Composition struct {
	preparer turnprocessing.StructuredInputPreparer
	custody  *mediaproduction.PhotoCustody
}

func Open(runtime *mediaproduction.Runtime) (*Composition, error) {
	if runtime == nil || runtime.Preparer == nil || runtime.Photos == nil {
		return nil, ErrInvalidRuntime
	}
	return &Composition{preparer: runtime.Preparer, custody: runtime.Photos}, nil
}

func (composition *Composition) PrepareStructured(ctx context.Context, input turnprocessing.IncomingInput) (turnprocessing.PreparedInput, error) {
	if composition == nil || composition.preparer == nil || ctx == nil {
		return turnprocessing.PreparedInput{}, ErrInvalidRuntime
	}
	prepared, err := composition.preparer.PrepareStructured(ctx, input)
	if err != nil {
		return turnprocessing.PreparedInput{}, err
	}
	for _, attachment := range prepared.Attachments {
		if !safeAttachment(attachment) {
			return turnprocessing.PreparedInput{}, ErrUnsafeAttachment
		}
	}
	return prepared, nil
}

// Prepare exists only to meet the controller's legacy input-preparer seam.
// It never serializes a custody reference into provider text.
func (composition *Composition) Prepare(ctx context.Context, input turnprocessing.IncomingInput) (string, error) {
	prepared, err := composition.PrepareStructured(ctx, input)
	if err != nil {
		return "", err
	}
	if len(prepared.Attachments) != 0 {
		return "", ErrStructuredAttachment
	}
	return prepared.Text, nil
}

func (composition *Composition) MarkAccepted(ctx context.Context, receipt turnprocessing.AttachmentReceipt) error {
	if composition == nil || composition.custody == nil {
		return ErrInvalidRuntime
	}
	return composition.custody.MarkAccepted(ctx, mediaproduction.AttachmentReceipt{
		Reference: receipt.Reference, ProviderSessionID: receipt.ProviderSession, MessageID: receipt.MessageID,
	})
}

func (composition *Composition) MarkCompleted(ctx context.Context, receipt turnprocessing.AttachmentReceipt) error {
	if composition == nil || composition.custody == nil {
		return ErrInvalidRuntime
	}
	return composition.custody.MarkCompleted(ctx, mediaproduction.AttachmentReceipt{
		Reference: receipt.Reference, ProviderSessionID: receipt.ProviderSession, MessageID: receipt.MessageID,
	})
}

func safeAttachment(attachment turnprocessing.AttachmentRef) bool {
	if attachment.Reference == "" || strings.TrimSpace(attachment.Reference) != attachment.Reference ||
		filepath.IsAbs(attachment.Reference) || strings.ContainsAny(attachment.Reference, `/\\`) || attachment.Size <= 0 || len(attachment.SHA256) != 64 {
		return false
	}
	for _, character := range attachment.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

var _ turnprocessing.InputPreparer = (*Composition)(nil)
var _ turnprocessing.StructuredInputPreparer = (*Composition)(nil)
var _ turnprocessing.AttachmentCustody = (*Composition)(nil)
