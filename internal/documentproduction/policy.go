// Package documentproduction adapts bounded Telegram documents to attachment custody.
package documentproduction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"bria/internal/mediaflow"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

// TextDocumentPolicy accepts bounded Telegram documents and stores their bytes
// in custody. Providers receive the verified local path, never file contents
// injected into the prompt.
type TextDocumentPolicy struct {
	Downloader mediaflow.Downloader
	Attacher   mediaflow.PhotoAttacher
	MaxBytes   int64
}

// WithDefaultAttacher binds runtime custody without replacing an explicit
// attacher or mutating the policy supplied by the caller.
func (policy TextDocumentPolicy) WithDefaultAttacher(attacher mediaflow.PhotoAttacher) mediaflow.DocumentPolicy {
	if policy.Attacher == nil {
		policy.Attacher = attacher
	}
	return policy
}

func (policy TextDocumentPolicy) PrepareDocument(ctx context.Context, input telegramcontroller.IncomingInput) (string, error) {
	return "", mediaflow.ErrDocumentPolicy
}

func (policy TextDocumentPolicy) PrepareDocumentStructured(ctx context.Context, input telegramcontroller.IncomingInput) (telegramcontroller.PreparedInput, error) {
	if policy.Attacher == nil {
		return telegramcontroller.PreparedInput{}, mediaflow.ErrDocumentPolicy
	}
	if policy.Downloader == nil || policy.MaxBytes <= 0 || input.Kind != string(telegram.MediaDocument) || !input.DownloadPermitted || input.FileSize < 0 || input.FileSize > policy.MaxBytes {
		return telegramcontroller.PreparedInput{}, mediaflow.ErrMediaTooLarge
	}
	download, err := policy.Downloader.DownloadMedia(ctx, telegram.DownloadMediaRequest{Kind: telegram.MediaDocument, FileID: input.FileID, MaxBytes: policy.MaxBytes})
	if err != nil {
		return telegramcontroller.PreparedInput{}, err
	}
	actualSize := int64(len(download.Content))
	if actualSize == 0 || actualSize > policy.MaxBytes || download.File.FileSize > policy.MaxBytes {
		return telegramcontroller.PreparedInput{}, mediaflow.ErrMediaTooLarge
	}
	if download.File.FileID != input.FileID ||
		(input.FileUniqueID != "" && download.File.FileUniqueID != input.FileUniqueID) ||
		(input.FileSize > 0 && actualSize != input.FileSize) ||
		(download.File.FileSize > 0 && actualSize != download.File.FileSize) {
		return telegramcontroller.PreparedInput{}, mediaflow.ErrDownloadMismatch
	}
	ref, err := policy.Attacher.AttachPhoto(ctx, mediaflow.PhotoAttachment{FileID: input.FileID, FileUniqueID: input.FileUniqueID, MIMEType: "application/octet-stream", Content: download.Content})
	if err != nil {
		return telegramcontroller.PreparedInput{}, err
	}
	digest := sha256.Sum256(download.Content)
	return telegramcontroller.PreparedInput{Attachments: []telegramcontroller.AttachmentRef{{Reference: ref, Size: actualSize, SHA256: hex.EncodeToString(digest[:])}}}, nil
}
