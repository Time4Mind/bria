package mediaflow_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"bria/internal/files"
	"bria/internal/mediaflow"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

type downloadFunc func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error)

func (function downloadFunc) DownloadMedia(ctx context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
	return function(ctx, request)
}

type recognizeFunc func(context.Context, string) (string, error)

func (function recognizeFunc) Transcribe(ctx context.Context, path string) (string, error) {
	return function(ctx, path)
}

type attachPhotoFunc func(context.Context, mediaflow.PhotoAttachment) (string, error)

func (function attachPhotoFunc) AttachPhoto(ctx context.Context, photo mediaflow.PhotoAttachment) (string, error) {
	return function(ctx, photo)
}

type photoCustody struct {
	attach func(context.Context, mediaflow.PhotoAttachment) (string, error)
	verify func(context.Context, string, mediaflow.PhotoDigest) error
}

func (custody photoCustody) AttachPhoto(ctx context.Context, photo mediaflow.PhotoAttachment) (string, error) {
	return custody.attach(ctx, photo)
}

func (custody photoCustody) VerifyPhoto(ctx context.Context, reference string, digest mediaflow.PhotoDigest) error {
	return custody.verify(ctx, reference, digest)
}

func verifiedPhotoAttacher(attach func(context.Context, mediaflow.PhotoAttachment) (string, error)) photoCustody {
	return photoCustody{
		attach: attach,
		verify: func(context.Context, string, mediaflow.PhotoDigest) error { return nil },
	}
}

type documentPolicyFunc func(context.Context, telegramcontroller.IncomingInput) (string, error)

func (function documentPolicyFunc) PrepareDocument(ctx context.Context, input telegramcontroller.IncomingInput) (string, error) {
	return function(ctx, input)
}

var _ telegramcontroller.InputPreparer = (*mediaflow.Preparer)(nil)

func TestVoiceDownloadsStagesTranscribesAndCleansUp(t *testing.T) {
	var request telegram.DownloadMediaRequest
	downloader := downloadFunc(func(_ context.Context, got telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		request = got
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: "voice-file", FileUniqueID: "voice-unique", FileSize: 11, FilePath: "voice/file.ogg"},
			Content: []byte("voice bytes"),
		}, nil
	})
	var stagedPath string
	recognizer := recognizeFunc(func(_ context.Context, path string) (string, error) {
		stagedPath = path
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read staged voice: %v", err)
		}
		if got := string(content); got != "voice bytes" {
			t.Fatalf("staged voice = %q", got)
		}
		return "  recognised locally  ", nil
	})
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		recognizer,
		nil,
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	prepared, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "voice", FileID: "voice-file", FileUniqueID: "voice-unique",
		FileSize: 11, MIMEType: "audio/ogg", DurationSeconds: 3, DownloadPermitted: true,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared != "recognised locally" {
		t.Fatalf("Prepare() = %q", prepared)
	}
	if request.Kind != telegram.MediaVoice || request.FileID != "voice-file" || request.MaxBytes != 32 {
		t.Fatalf("download request = %#v", request)
	}
	if stagedPath == "" {
		t.Fatal("recognizer did not receive staged path")
	}
	if _, err := os.Stat(stagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged voice still exists after Prepare: %v", err)
	}
}

func TestPhotoIsDownloadedAndAttachedWithoutCaption(t *testing.T) {
	var downloadCalls int
	downloader := downloadFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		downloadCalls++
		if request.Kind != telegram.MediaPhoto || request.FileID != "photo-file" || request.MaxBytes != 64 {
			t.Fatalf("download request = %#v", request)
		}
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: "photo-file", FileUniqueID: "photo-unique", FileSize: 5, FilePath: "photos/p.jpg"},
			Content: []byte("photo"),
		}, nil
	})
	var attached mediaflow.PhotoAttachment
	var verifiedReference string
	var verifiedDigest mediaflow.PhotoDigest
	attacher := photoCustody{
		attach: func(_ context.Context, photo mediaflow.PhotoAttachment) (string, error) {
			attached = photo
			return "bria-photo-ref:sha256:abc", nil
		},
		verify: func(_ context.Context, reference string, digest mediaflow.PhotoDigest) error {
			verifiedReference = reference
			verifiedDigest = digest
			return nil
		},
	}
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		nil,
		attacher,
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	prepared, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "photo", FileID: "photo-file", FileUniqueID: "photo-unique", FileSize: 5,
		Width: 1280, Height: 720, DownloadPermitted: true,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared != "bria-photo-ref:sha256:abc" || downloadCalls != 1 {
		t.Fatalf("Prepare() = %q after %d downloads", prepared, downloadCalls)
	}
	if attached.FileID != "photo-file" || attached.FileUniqueID != "photo-unique" || attached.MIMEType != "image/jpeg" ||
		attached.Width != 1280 || attached.Height != 720 || string(attached.Content) != "photo" {
		t.Fatalf("photo attachment = %#v", attached)
	}
	if verifiedReference != prepared || verifiedDigest.Size != 5 ||
		verifiedDigest.SHA256 != "55c64d0fcd6f9d5f7c828093857e3fdfda68478bb4e9bd24d481ef391c7804e8" {
		t.Fatalf("verified photo = (%q, %#v)", verifiedReference, verifiedDigest)
	}
}

func TestPhotoRejectsReferenceWithoutVerifiedCustody(t *testing.T) {
	downloader := downloadFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: request.FileID, FileUniqueID: "unique", FileSize: 5, FilePath: "p"},
			Content: []byte("photo"),
		}, nil
	})
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		nil,
		attachPhotoFunc(func(context.Context, mediaflow.PhotoAttachment) (string, error) {
			return "ephemeral-reference", nil
		}),
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if reference, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "photo", FileID: "photo", FileUniqueID: "unique", FileSize: 5, DownloadPermitted: true,
	}); err == nil || reference != "" {
		t.Fatalf("Prepare(ephemeral photo) = %q, %v", reference, err)
	}
}

func TestPhotoRejectsCustodyThatCannotRereadReference(t *testing.T) {
	downloader := downloadFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: request.FileID, FileUniqueID: "unique", FileSize: 5, FilePath: "p"},
			Content: []byte("photo"),
		}, nil
	})
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		nil,
		photoCustody{
			attach: func(context.Context, mediaflow.PhotoAttachment) (string, error) { return "lost-reference", nil },
			verify: func(context.Context, string, mediaflow.PhotoDigest) error { return errors.New("not found after write") },
		},
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if reference, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "photo", FileID: "photo", FileUniqueID: "unique", FileSize: 5, DownloadPermitted: true,
	}); err == nil || reference != "" {
		t.Fatalf("Prepare(lost photo) = %q, %v", reference, err)
	}
}

func TestVoiceTemporaryIsCleanedAfterRecognitionFailure(t *testing.T) {
	downloader := downloadFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: request.FileID, FileUniqueID: "voice-unique", FileSize: 5, FilePath: "voice/f.ogg"},
			Content: []byte("voice"),
		}, nil
	})
	var stagedPath string
	recognizer := recognizeFunc(func(_ context.Context, path string) (string, error) {
		stagedPath = path
		return "", errors.New("local recognizer failed")
	})
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		recognizer,
		nil,
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if prepared, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "voice", FileID: "voice-file", FileUniqueID: "voice-unique", FileSize: 5, DownloadPermitted: true,
	}); err == nil || prepared != "" {
		t.Fatalf("Prepare() = %q, %v", prepared, err)
	}
	if stagedPath == "" {
		t.Fatal("recognizer did not receive staged path")
	}
	if _, err := os.Stat(stagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged voice still exists after failed recognition: %v", err)
	}
}

func TestVoiceReportsCleanupFailureTogetherWithRecognitionFailure(t *testing.T) {
	downloader := downloadFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: request.FileID, FileUniqueID: "voice-unique", FileSize: 5, FilePath: "voice/f.ogg"},
			Content: []byte("voice"),
		}, nil
	})
	var stagedPath string
	recognizer := recognizeFunc(func(_ context.Context, path string) (string, error) {
		stagedPath = path
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove staged test file: %v", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("replace staged file with directory: %v", err)
		}
		if err := os.WriteFile(path+"/held", []byte("x"), 0o600); err != nil {
			t.Fatalf("make cleanup fail: %v", err)
		}
		return "", errors.New("recognition failed")
	})
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		recognizer,
		nil,
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, prepareErr := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "voice", FileID: "voice-file", FileUniqueID: "voice-unique", FileSize: 5, DownloadPermitted: true,
	})
	if stagedPath != "" {
		t.Cleanup(func() { _ = os.RemoveAll(stagedPath) })
	}
	if prepareErr == nil || !strings.Contains(prepareErr.Error(), "transcribe staged voice input") ||
		!strings.Contains(prepareErr.Error(), "clean staged voice input") {
		t.Fatalf("Prepare() error = %v, want recognition and cleanup failures", prepareErr)
	}
}

func TestVideoAndUnknownMediaNeverDownload(t *testing.T) {
	downloader := downloadFunc(func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		t.Fatal("download called")
		return telegram.DownloadedMedia{}, nil
	})
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		nil,
		nil,
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, kind := range []string{"video", "animation", "audio"} {
		t.Run(kind, func(t *testing.T) {
			if prepared, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
				Kind: kind, FileID: "remote", FileSize: 8,
			}); err == nil || prepared != "" {
				t.Fatalf("Prepare(%q) = %q, %v", kind, prepared, err)
			}
		})
	}
}

func TestDocumentRequiresExplicitSafePolicy(t *testing.T) {
	downloader := downloadFunc(func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		t.Fatal("document used Telegram media downloader")
		return telegram.DownloadedMedia{}, nil
	})
	limits := mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256}
	input := telegramcontroller.IncomingInput{Kind: "document", FileID: "document-file", FileSize: 8}
	withoutPolicy, err := mediaflow.New(downloader, files.Stager{Directory: t.TempDir(), MaxBytes: 32}, nil, nil, nil, limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if prepared, err := withoutPolicy.Prepare(context.Background(), input); err == nil || prepared != "" {
		t.Fatalf("Prepare(document without policy) = %q, %v", prepared, err)
	}

	var policyCalls int
	policy := documentPolicyFunc(func(_ context.Context, got telegramcontroller.IncomingInput) (string, error) {
		policyCalls++
		if got != input {
			t.Fatalf("document policy input = %#v", got)
		}
		return "safe-document-ref:sha256:def", nil
	})
	withPolicy, err := mediaflow.New(downloader, files.Stager{Directory: t.TempDir(), MaxBytes: 32}, nil, nil, policy, limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	prepared, err := withPolicy.Prepare(context.Background(), input)
	if err != nil {
		t.Fatalf("Prepare(document) error = %v", err)
	}
	if prepared != "safe-document-ref:sha256:def" || policyCalls != 1 {
		t.Fatalf("Prepare(document) = %q after %d policy calls", prepared, policyCalls)
	}
}

func TestPreparationRejectsOversizeAndMismatchedDownloads(t *testing.T) {
	var calls int
	downloader := downloadFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		calls++
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: request.FileID + "-replacement", FileUniqueID: "other", FileSize: 3, FilePath: "media/file"},
			Content: []byte("abc"),
		}, nil
	})
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		recognizeFunc(func(context.Context, string) (string, error) { return "text", nil }),
		verifiedPhotoAttacher(func(context.Context, mediaflow.PhotoAttachment) (string, error) { return "ref", nil }),
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "voice", FileID: "too-large", FileSize: 33, DownloadPermitted: true,
	}); err == nil {
		t.Fatal("oversize declared voice accepted")
	}
	if calls != 0 {
		t.Fatalf("oversize declared voice caused %d downloads", calls)
	}
	if _, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "photo", FileID: "photo", FileUniqueID: "expected", FileSize: 3, DownloadPermitted: true,
	}); err == nil {
		t.Fatal("mismatched downloaded photo identity accepted")
	}
	if calls != 1 {
		t.Fatalf("mismatched photo caused %d downloads", calls)
	}
}

func TestPreparationRejectsTruncatedDownload(t *testing.T) {
	var attachCalls int
	preparer, err := mediaflow.New(
		downloadFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
			return telegram.DownloadedMedia{
				File:    telegram.File{FileID: request.FileID, FileUniqueID: "unique", FileSize: 5, FilePath: "p"},
				Content: []byte("four"),
			}, nil
		}),
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		nil,
		verifiedPhotoAttacher(func(context.Context, mediaflow.PhotoAttachment) (string, error) {
			attachCalls++
			return "ref", nil
		}),
		nil,
		mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 256},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "photo", FileID: "photo", FileUniqueID: "unique", FileSize: 5, DownloadPermitted: true,
	}); err == nil {
		t.Fatal("truncated photo download accepted")
	}
	if attachCalls != 0 {
		t.Fatalf("truncated photo reached attacher %d times", attachCalls)
	}
}

func TestConstructorAndPreparedOutputAreStrictlyBounded(t *testing.T) {
	valid := mediaflow.Limits{VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 8}
	if _, err := mediaflow.New(nil, files.Stager{}, nil, nil, nil, valid); err == nil {
		t.Fatal("nil downloader accepted")
	}
	var typedNil downloadFunc
	if _, err := mediaflow.New(typedNil, files.Stager{MaxBytes: 32}, nil, nil, nil, valid); err == nil {
		t.Fatal("typed-nil downloader accepted")
	}
	if _, err := mediaflow.New(downloadFunc(nil), files.Stager{}, nil, nil, nil, mediaflow.Limits{}); err == nil {
		t.Fatal("zero limits accepted")
	}
	preparer, err := mediaflow.New(
		downloadFunc(func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
			return telegram.DownloadedMedia{File: telegram.File{FileID: "photo", FileUniqueID: "unique", FileSize: 1, FilePath: "p"}, Content: []byte("x")}, nil
		}),
		files.Stager{Directory: t.TempDir(), MaxBytes: 32},
		nil,
		verifiedPhotoAttacher(func(context.Context, mediaflow.PhotoAttachment) (string, error) {
			return strings.Repeat("x", 9), nil
		}),
		nil,
		valid,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "photo", FileID: "photo", FileUniqueID: "unique", FileSize: 1, DownloadPermitted: true,
	}); err == nil {
		t.Fatal("oversize prepared reference accepted")
	}
}
