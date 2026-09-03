// Package mediaflow prepares bounded Telegram media for provider-neutral input.
package mediaflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"bria/internal/files"
	"bria/internal/speech"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

var (
	ErrInvalidConfiguration = errors.New("media input preparer configuration is invalid")
	ErrInvalidInput         = errors.New("media input descriptor is invalid")
	ErrDownloadForbidden    = errors.New("media download is forbidden")
	ErrMediaTooLarge        = errors.New("media input exceeds its configured bound")
	ErrDownloadMismatch     = errors.New("downloaded media identity does not match the input")
	ErrUnsupportedMedia     = errors.New("media input must be handled as text only")
	ErrDocumentPolicy       = errors.New("document input requires an explicit safe policy")
	ErrPreparedTooLarge     = errors.New("prepared media reference exceeds its configured bound")
	ErrPhotoCustody         = errors.New("photo reference custody could not be verified")
)

type Downloader interface {
	DownloadMedia(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error)
}

type PhotoAttachment struct {
	FileID       string
	FileUniqueID string
	MIMEType     string
	Width        int
	Height       int
	Content      []byte
}

// PhotoAttacher transfers bounded photo bytes into the provider-specific
// attachment boundary and returns the reference placed in the queued payload.
// Content is only guaranteed to remain valid for the duration of AttachPhoto;
// the implementation owns any longer-lived copy and its cleanup.
type PhotoAttacher interface {
	AttachPhoto(context.Context, PhotoAttachment) (string, error)
}

type PhotoDigest struct {
	Size   int64
	SHA256 string
}

// PhotoCustody must durably retain a reference beyond AttachPhoto and prove it
// can reread the exact bytes before Prepare gives the reference to the durable
// input queue. The custody implementation owns retention through provider
// handoff; transient in-memory attachment implementations are rejected.
type PhotoCustody interface {
	PhotoAttacher
	VerifyPhoto(context.Context, string, PhotoDigest) error
}

// DocumentPolicy is deliberately separate from Downloader. Documents are not
// downloaded unless composition explicitly supplies a policy that does so.
type DocumentPolicy interface {
	PrepareDocument(context.Context, telegramcontroller.IncomingInput) (string, error)
}

type Limits struct {
	VoiceBytes    int64
	PhotoBytes    int64
	PreparedBytes int
}

type Preparer struct {
	downloader Downloader
	stager     files.Stager
	recognizer speech.Recognizer
	photos     PhotoAttacher
	documents  DocumentPolicy
	limits     Limits
}

func New(
	downloader Downloader,
	stager files.Stager,
	recognizer speech.Recognizer,
	photos PhotoAttacher,
	documents DocumentPolicy,
	limits Limits,
) (*Preparer, error) {
	if isNilPort(downloader) || limits.VoiceBytes <= 0 || limits.PhotoBytes <= 0 || limits.PreparedBytes <= 0 ||
		stager.MaxBytes <= 0 || stager.MaxBytes < limits.VoiceBytes {
		return nil, ErrInvalidConfiguration
	}
	return &Preparer{
		downloader: downloader,
		stager:     stager,
		recognizer: recognizer,
		photos:     photos,
		documents:  documents,
		limits:     limits,
	}, nil
}

// Prepare implements telegramcontroller.InputPreparer. Voice and photo bytes
// are downloaded under a per-kind bound. Video and unknown media are rejected
// before the downloader is called; their caption is handled by Controller.
func (preparer *Preparer) Prepare(ctx context.Context, input telegramcontroller.IncomingInput) (string, error) {
	if preparer == nil || ctx == nil || !validInput(input) {
		return "", ErrInvalidInput
	}

	var (
		prepared string
		err      error
	)
	switch input.Kind {
	case string(telegram.MediaVoice):
		prepared, err = preparer.prepareVoice(ctx, input)
	case string(telegram.MediaPhoto):
		prepared, err = preparer.preparePhoto(ctx, input)
	case "document":
		if isNilPort(preparer.documents) {
			return "", ErrDocumentPolicy
		}
		prepared, err = preparer.documents.PrepareDocument(ctx, input)
	case string(telegram.MediaVideo):
		return "", ErrDownloadForbidden
	default:
		return "", ErrUnsupportedMedia
	}
	if err != nil {
		return "", err
	}
	return preparer.validatePrepared(prepared)
}

// PrepareStructured preserves durable photo custody identity separately from
// provider prompt text. Voice and document policies continue to return text;
// no filesystem path is inferred from their output.
func (preparer *Preparer) PrepareStructured(ctx context.Context, input telegramcontroller.IncomingInput) (telegramcontroller.PreparedInput, error) {
	if preparer == nil || ctx == nil || !validInput(input) {
		return telegramcontroller.PreparedInput{}, ErrInvalidInput
	}
	if input.Kind != string(telegram.MediaPhoto) {
		text, err := preparer.Prepare(ctx, input)
		return telegramcontroller.PreparedInput{Text: text}, err
	}
	reference, digest, err := preparer.preparePhotoAttachment(ctx, input)
	if err != nil {
		return telegramcontroller.PreparedInput{}, err
	}
	return telegramcontroller.PreparedInput{Attachments: []telegramcontroller.AttachmentRef{{
		Reference: reference, Size: digest.Size, SHA256: digest.SHA256,
	}}}, nil
}

func (preparer *Preparer) prepareVoice(ctx context.Context, input telegramcontroller.IncomingInput) (prepared string, err error) {
	if isNilPort(preparer.recognizer) {
		return "", ErrInvalidConfiguration
	}
	download, err := preparer.download(ctx, input, telegram.MediaVoice, preparer.limits.VoiceBytes)
	if err != nil {
		return "", err
	}
	temporary, err := preparer.stager.Stage(bytes.NewReader(download.Content))
	if err != nil {
		return "", fmt.Errorf("stage bounded voice input: %w", err)
	}
	defer func() {
		if cleanupErr := temporary.Cleanup(); cleanupErr != nil {
			prepared = ""
			err = errors.Join(err, fmt.Errorf("clean staged voice input: %w", cleanupErr))
		}
	}()
	prepared, err = preparer.recognizer.Transcribe(ctx, temporary.Path())
	if err != nil {
		return "", fmt.Errorf("transcribe staged voice input: %w", err)
	}
	return prepared, nil
}

func (preparer *Preparer) preparePhoto(ctx context.Context, input telegramcontroller.IncomingInput) (string, error) {
	reference, _, err := preparer.preparePhotoAttachment(ctx, input)
	return reference, err
}

func (preparer *Preparer) preparePhotoAttachment(ctx context.Context, input telegramcontroller.IncomingInput) (string, PhotoDigest, error) {
	if isNilPort(preparer.photos) {
		return "", PhotoDigest{}, ErrInvalidConfiguration
	}
	custody, ok := preparer.photos.(PhotoCustody)
	if !ok || isNilPort(custody) {
		return "", PhotoDigest{}, ErrPhotoCustody
	}
	download, err := preparer.download(ctx, input, telegram.MediaPhoto, preparer.limits.PhotoBytes)
	if err != nil {
		return "", PhotoDigest{}, err
	}
	mimeType := input.MIMEType
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	if !validPhotoMIME(mimeType) {
		return "", PhotoDigest{}, ErrInvalidInput
	}
	content := append([]byte(nil), download.Content...)
	reference, err := preparer.photos.AttachPhoto(ctx, PhotoAttachment{
		FileID:       input.FileID,
		FileUniqueID: input.FileUniqueID,
		MIMEType:     mimeType,
		Width:        input.Width,
		Height:       input.Height,
		Content:      content,
	})
	if err != nil {
		return "", PhotoDigest{}, fmt.Errorf("attach bounded photo input: %w", err)
	}
	reference, err = preparer.validatePrepared(reference)
	if err != nil {
		return "", PhotoDigest{}, err
	}
	digestBytes := sha256.Sum256(download.Content)
	digest := PhotoDigest{Size: int64(len(download.Content)), SHA256: hex.EncodeToString(digestBytes[:])}
	if err := custody.VerifyPhoto(ctx, reference, digest); err != nil {
		return "", PhotoDigest{}, errors.Join(ErrPhotoCustody, err)
	}
	return reference, digest, nil
}

func (preparer *Preparer) download(
	ctx context.Context,
	input telegramcontroller.IncomingInput,
	kind telegram.MediaKind,
	maxBytes int64,
) (telegram.DownloadedMedia, error) {
	if !input.DownloadPermitted {
		return telegram.DownloadedMedia{}, ErrDownloadForbidden
	}
	if input.FileSize > maxBytes {
		return telegram.DownloadedMedia{}, ErrMediaTooLarge
	}
	download, err := preparer.downloader.DownloadMedia(ctx, telegram.DownloadMediaRequest{
		Kind: kind, FileID: input.FileID, MaxBytes: maxBytes,
	})
	if err != nil {
		return telegram.DownloadedMedia{}, fmt.Errorf("download bounded media input: %w", err)
	}
	if download.File.FileID != input.FileID ||
		(input.FileUniqueID != "" && download.File.FileUniqueID != input.FileUniqueID) {
		return telegram.DownloadedMedia{}, ErrDownloadMismatch
	}
	contentBytes := int64(len(download.Content))
	if download.File.FileSize < 0 || download.File.FileSize > maxBytes || contentBytes == 0 || contentBytes > maxBytes {
		return telegram.DownloadedMedia{}, ErrMediaTooLarge
	}
	if (download.File.FileSize > 0 && download.File.FileSize != contentBytes) ||
		(input.FileSize > 0 && input.FileSize != contentBytes) {
		return telegram.DownloadedMedia{}, ErrDownloadMismatch
	}
	return download, nil
}

func (preparer *Preparer) validatePrepared(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", ErrInvalidInput
	}
	if len(value) > preparer.limits.PreparedBytes {
		return "", ErrPreparedTooLarge
	}
	return value, nil
}

func validInput(input telegramcontroller.IncomingInput) bool {
	if strings.TrimSpace(input.Kind) != input.Kind || input.Kind == "" ||
		strings.TrimSpace(input.FileID) != input.FileID || input.FileID == "" ||
		strings.ContainsRune(input.Kind, 0) || strings.ContainsRune(input.FileID, 0) ||
		strings.ContainsRune(input.FileUniqueID, 0) || strings.ContainsRune(input.MIMEType, 0) {
		return false
	}
	return input.FileSize >= 0 && input.DurationSeconds >= 0 && input.Width >= 0 && input.Height >= 0
}

func validPhotoMIME(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func isNilPort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ telegramcontroller.InputPreparer = (*Preparer)(nil)
