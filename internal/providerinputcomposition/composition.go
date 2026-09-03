// Package providerinputcomposition resolves durable attachment custody at the
// provider boundary without flattening local paths into prompt text.
package providerinputcomposition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

var (
	ErrInvalidConfiguration           = errors.New("provider input composition is invalid")
	ErrAttachmentUnverifiable         = errors.New("attachment is unverifiable")
	ErrProviderAttachmentsUnsupported = errors.New("provider does not support structured attachments")
	ErrSessionProviderUnverifiable    = errors.New("session provider is unverifiable")
)

type AttachmentResolver interface {
	ResolveAttachment(context.Context, string) (string, error)
}

type SessionProviderResolver interface {
	ProviderForSession(context.Context, domain.SessionID) (domain.Provider, error)
}

type Runtime interface {
	sessionruntime.Submitter
	sessionruntime.InteractiveSubmitter
	sessionruntime.StructuredSubmitter
}

type Submitter struct {
	runtime   Runtime
	resolver  AttachmentResolver
	providers SessionProviderResolver
}

func New(runtime Runtime, resolver AttachmentResolver, providers SessionProviderResolver) (*Submitter, error) {
	if runtime == nil || resolver == nil || providers == nil {
		return nil, ErrInvalidConfiguration
	}
	return &Submitter{runtime: runtime, resolver: resolver, providers: providers}, nil
}

func (submitter *Submitter) Submit(ctx context.Context, sessionID domain.SessionID, text string) (sessionruntime.TurnResult, error) {
	if submitter == nil || submitter.runtime == nil || submitter.providers == nil || ctx == nil || sessionID == "" {
		return sessionruntime.TurnResult{}, ErrInvalidConfiguration
	}
	if _, err := submitter.providerForSession(ctx, sessionID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	return submitter.runtime.Submit(ctx, sessionID, text)
}

func (submitter *Submitter) SubmitWithCallbacks(ctx context.Context, sessionID domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if submitter == nil || submitter.runtime == nil || submitter.providers == nil || ctx == nil || sessionID == "" {
		return sessionruntime.TurnResult{}, ErrInvalidConfiguration
	}
	if _, err := submitter.providerForSession(ctx, sessionID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	return submitter.runtime.SubmitWithCallbacks(ctx, sessionID, text, callbacks)
}

func (submitter *Submitter) SubmitPreparedWithCallbacks(ctx context.Context, sessionID domain.SessionID, input turnprocessing.PreparedInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if submitter == nil || submitter.runtime == nil || submitter.resolver == nil || submitter.providers == nil || ctx == nil || sessionID == "" || strings.TrimSpace(callbacks.MessageID) == "" {
		return sessionruntime.TurnResult{}, ErrInvalidConfiguration
	}
	provider, err := submitter.providerForSession(ctx, sessionID)
	if err != nil {
		return sessionruntime.TurnResult{}, err
	}
	if len(input.Attachments) == 0 {
		return submitter.runtime.SubmitWithCallbacks(ctx, sessionID, input.Text, callbacks)
	}
	if provider != domain.ProviderCodex {
		return sessionruntime.TurnResult{}, ErrProviderAttachmentsUnsupported
	}
	if len(input.Attachments) > 8 {
		return sessionruntime.TurnResult{}, ErrInvalidConfiguration
	}
	attachments := make([]sessionruntime.LocalAttachment, 0, len(input.Attachments))
	seen := make(map[string]struct{}, len(input.Attachments))
	for _, attachment := range input.Attachments {
		if !validReference(attachment) {
			return sessionruntime.TurnResult{}, ErrAttachmentUnverifiable
		}
		if _, duplicate := seen[attachment.Reference]; duplicate {
			return sessionruntime.TurnResult{}, ErrAttachmentUnverifiable
		}
		seen[attachment.Reference] = struct{}{}
		path, err := submitter.resolver.ResolveAttachment(ctx, attachment.Reference)
		if err != nil || verifyAttachment(ctx, path, attachment) != nil {
			return sessionruntime.TurnResult{}, ErrAttachmentUnverifiable
		}
		attachments = append(attachments, sessionruntime.LocalAttachment{
			Path: path, Size: attachment.Size, SHA256: attachment.SHA256,
		})
	}
	return submitter.runtime.SubmitStructuredWithCallbacks(ctx, sessionID, sessionruntime.StructuredInput{
		Text: input.Text, Attachments: attachments,
	}, callbacks)
}

func (submitter *Submitter) providerForSession(ctx context.Context, sessionID domain.SessionID) (domain.Provider, error) {
	provider, err := submitter.providers.ProviderForSession(ctx, sessionID)
	if err != nil || provider != domain.ProviderCodex && provider != domain.ProviderClaude {
		return "", ErrSessionProviderUnverifiable
	}
	return provider, nil
}

func validReference(attachment turnprocessing.AttachmentRef) bool {
	if attachment.Reference == "" || len(attachment.Reference) > 512 || !utf8.ValidString(attachment.Reference) || strings.TrimSpace(attachment.Reference) != attachment.Reference ||
		filepath.IsAbs(attachment.Reference) || strings.ContainsAny(attachment.Reference, `/\`) ||
		attachment.Size < 1 || attachment.Size > 32<<20 || len(attachment.SHA256) != 64 {
		return false
	}
	for _, character := range attachment.Reference {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	for _, character := range attachment.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func verifyAttachment(ctx context.Context, path string, expected turnprocessing.AttachmentRef) error {
	if !filepath.IsAbs(path) || strings.TrimSpace(path) != path || strings.ContainsRune(path, '\x00') {
		return ErrAttachmentUnverifiable
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() != expected.Size {
		return ErrAttachmentUnverifiable
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrAttachmentUnverifiable
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			size += int64(read)
			_, _ = hash.Write(buffer[:read])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil || size > expected.Size {
			_ = file.Close()
			return ErrAttachmentUnverifiable
		}
	}
	after, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || !os.SameFile(before, after) || after.Size() != expected.Size || closeErr != nil ||
		size != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return ErrAttachmentUnverifiable
	}
	return nil
}

var _ turnprocessing.PreparedTurnSubmitter = (*Submitter)(nil)
var _ sessionruntime.InteractiveSubmitter = (*Submitter)(nil)
