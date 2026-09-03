package mediaproduction_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bria/internal/mediaflow"
	"bria/internal/mediaproduction"
	"bria/internal/speech/parakeet"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

type downloaderFunc func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error)

func (function downloaderFunc) DownloadMedia(ctx context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
	return function(ctx, request)
}

type documentPolicyFunc func(context.Context, telegramcontroller.IncomingInput) (string, error)

func (function documentPolicyFunc) PrepareDocument(ctx context.Context, input telegramcontroller.IncomingInput) (string, error) {
	return function(ctx, input)
}

func TestRuntimeVoiceUsesBoundedDownloaderLocalParakeetAndTemporaryCleanup(t *testing.T) {
	if os.Getenv("BRIA_MEDIA_HELPER") == "transcribe" {
		audioPath := os.Args[len(os.Args)-1]
		content, err := os.ReadFile(audioPath)
		if err != nil || string(content) != "voice bytes" {
			os.Exit(31)
		}
		if err := os.WriteFile(os.Getenv("BRIA_MEDIA_PATH_RECEIPT"), []byte(audioPath), 0o600); err != nil {
			os.Exit(32)
		}
		fmt.Print(" recognised locally \n")
		os.Exit(0)
	}

	receiptPath := filepath.Join(t.TempDir(), "path-receipt")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	var gotRequest telegram.DownloadMediaRequest
	downloader := downloaderFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		gotRequest = request
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: "voice-file", FileUniqueID: "voice-unique", FileSize: 11, FilePath: "voice/f.ogg"},
			Content: []byte("voice bytes"),
		}, nil
	})
	runtime, err := mediaproduction.Open(downloader, mediaproduction.Config{
		VoiceTempDirectory: filepath.Join(canonicalTempDir(t), "voice"),
		PhotoDirectory:     filepath.Join(canonicalTempDir(t), "photos"),
		VoiceBytes:         32,
		PhotoBytes:         64,
		PreparedBytes:      1024,
		Parakeet: parakeet.Command{
			Executable:         executable,
			ModelPath:          regularModelFile(t),
			Arguments:          []string{"-test.run=TestRuntimeVoiceUsesBoundedDownloaderLocalParakeetAndTemporaryCleanup", parakeet.ModelPathPlaceholder},
			Environment:        []string{"BRIA_MEDIA_HELPER=transcribe", "BRIA_MEDIA_PATH_RECEIPT=" + receiptPath},
			MaxTranscriptBytes: 128,
			MaxDiagnosticBytes: 128,
		},
		DocumentMode: mediaproduction.DocumentsReject,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	prepared, err := runtime.Preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "voice", FileID: "voice-file", FileUniqueID: "voice-unique", FileSize: 11,
		MIMEType: "audio/ogg", DurationSeconds: 2, DownloadPermitted: true,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared != "recognised locally" {
		t.Fatalf("Prepare() = %q", prepared)
	}
	if gotRequest != (telegram.DownloadMediaRequest{Kind: telegram.MediaVoice, FileID: "voice-file", MaxBytes: 32}) {
		t.Fatalf("download request = %#v", gotRequest)
	}
	stagedPathBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("read helper receipt: %v", err)
	}
	if _, err := os.Stat(string(stagedPathBytes)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("voice temporary still exists: %v", err)
	}
}

func TestPhotoCustodySurvivesReopenAndReleasesOnlyAfterExactProviderCompletion(t *testing.T) {
	directory := filepath.Join(canonicalTempDir(t), "photos")
	store, err := mediaproduction.OpenPhotoCustody(directory, 64)
	if err != nil {
		t.Fatalf("OpenPhotoCustody() error = %v", err)
	}
	content := []byte("photo bytes")
	reference, err := store.AttachPhoto(context.Background(), mediaflow.PhotoAttachment{
		FileID: "telegram-file", FileUniqueID: "telegram-unique", MIMEType: "image/jpeg",
		Width: 800, Height: 600, Content: content,
	})
	if err != nil {
		t.Fatalf("AttachPhoto() error = %v", err)
	}
	if len(reference) != 32 || strings.ContainsAny(reference, `/\\`) || filepath.IsAbs(reference) {
		t.Fatalf("reference = %q", reference)
	}
	digestBytes := sha256.Sum256(content)
	digest := mediaflow.PhotoDigest{Size: int64(len(content)), SHA256: hex.EncodeToString(digestBytes[:])}
	if err := store.VerifyPhoto(context.Background(), reference, digest); err != nil {
		t.Fatalf("VerifyPhoto() error = %v", err)
	}

	reopened, err := mediaproduction.OpenPhotoCustody(directory, 64)
	if err != nil {
		t.Fatalf("reopen custody error = %v", err)
	}
	receipt := mediaproduction.AttachmentReceipt{Reference: reference, ProviderSessionID: "provider-session", MessageID: "telegram-update:42"}
	if err := reopened.MarkAccepted(context.Background(), receipt); err != nil {
		t.Fatalf("MarkAccepted() error = %v", err)
	}
	wrong := receipt
	wrong.MessageID = "telegram-update:43"
	if err := reopened.MarkCompleted(context.Background(), wrong); !errors.Is(err, mediaproduction.ErrReceiptMismatch) {
		t.Fatalf("MarkCompleted(wrong) error = %v", err)
	}
	payload := filepath.Join(directory, reference, "photo")
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("photo removed before exact completion: %v", err)
	}
	if err := reopened.MarkCompleted(context.Background(), receipt); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	if _, err := os.Stat(payload); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released photo still exists: %v", err)
	}
	status, err := reopened.Status(context.Background(), reference)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != mediaproduction.PhotoReleased {
		t.Fatalf("Status() = %q", status)
	}
	if err := reopened.MarkCompleted(context.Background(), receipt); err != nil {
		t.Fatalf("idempotent MarkCompleted() error = %v", err)
	}
}

func TestPhotoCustodyDetectsContentReplacementBeforeHandoff(t *testing.T) {
	directory := filepath.Join(canonicalTempDir(t), "photos")
	store, err := mediaproduction.OpenPhotoCustody(directory, 64)
	if err != nil {
		t.Fatalf("OpenPhotoCustody() error = %v", err)
	}
	reference, err := store.AttachPhoto(context.Background(), mediaflow.PhotoAttachment{
		FileID: "file", FileUniqueID: "unique", MIMEType: "image/png", Content: []byte("original"),
	})
	if err != nil {
		t.Fatalf("AttachPhoto() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, reference, "photo"), []byte("replaced"), 0o600); err != nil {
		t.Fatalf("replace photo: %v", err)
	}
	digest := sha256.Sum256([]byte("original"))
	err = store.VerifyPhoto(context.Background(), reference, mediaflow.PhotoDigest{Size: 8, SHA256: hex.EncodeToString(digest[:])})
	if !errors.Is(err, mediaproduction.ErrPhotoCorrupt) {
		t.Fatalf("VerifyPhoto(replaced) error = %v", err)
	}
}

func TestPhotoCustodyResolveAttachmentOnlyReturnsIntactActiveCustody(t *testing.T) {
	directory := filepath.Join(canonicalTempDir(t), "photos")
	store, err := mediaproduction.OpenPhotoCustody(directory, 64)
	if err != nil {
		t.Fatalf("OpenPhotoCustody() error = %v", err)
	}
	content := []byte("original")
	reference, err := store.AttachPhoto(context.Background(), mediaflow.PhotoAttachment{
		FileID: "file", FileUniqueID: "unique", MIMEType: "image/png", Content: content,
	})
	if err != nil {
		t.Fatalf("AttachPhoto() error = %v", err)
	}
	want := filepath.Join(directory, reference, "photo")
	path, err := store.ResolveAttachment(context.Background(), reference)
	if err != nil || path != want || !filepath.IsAbs(path) {
		t.Fatalf("ResolveAttachment() = %q, %v; want %q", path, err, want)
	}
	receipt := mediaproduction.AttachmentReceipt{Reference: reference, ProviderSessionID: "provider", MessageID: "message"}
	if err := store.MarkAccepted(context.Background(), receipt); err != nil {
		t.Fatalf("MarkAccepted() error = %v", err)
	}
	if path, err := store.ResolveAttachment(context.Background(), reference); err != nil || path != want {
		t.Fatalf("accepted ResolveAttachment() = %q, %v", path, err)
	}
	reopened, err := mediaproduction.OpenPhotoCustody(directory, 64)
	if err != nil {
		t.Fatalf("reopen custody error = %v", err)
	}
	if path, err := reopened.ResolveAttachment(context.Background(), reference); err != nil || path != want {
		t.Fatalf("reopened ResolveAttachment() = %q, %v", path, err)
	}
}

func TestPhotoCustodyResolveAttachmentRejectsUnsafeCustody(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *mediaproduction.PhotoCustody, string, string)
	}{
		{name: "payload replacement", mutate: func(t *testing.T, _ *mediaproduction.PhotoCustody, _ string, directory string) {
			if err := os.WriteFile(filepath.Join(directory, "photo"), []byte("replaced"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "payload truncation", mutate: func(t *testing.T, _ *mediaproduction.PhotoCustody, _ string, directory string) {
			if err := os.WriteFile(filepath.Join(directory, "photo"), []byte("tiny"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "payload symlink", mutate: func(t *testing.T, _ *mediaproduction.PhotoCustody, _ string, directory string) {
			target := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(directory, "photo")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(directory, "photo")); err != nil {
				t.Skipf("symlink is unavailable: %v", err)
			}
		}},
		{name: "metadata mismatch", mutate: func(t *testing.T, _ *mediaproduction.PhotoCustody, _ string, directory string) {
			path := filepath.Join(directory, "custody.json")
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Replace(string(encoded), `"size":8`, `"size":9`, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "completed", mutate: func(t *testing.T, store *mediaproduction.PhotoCustody, reference, _ string) {
			receipt := mediaproduction.AttachmentReceipt{Reference: reference, ProviderSessionID: "provider", MessageID: "message"}
			if err := store.MarkAccepted(context.Background(), receipt); err != nil {
				t.Fatal(err)
			}
			if err := store.MarkCompleted(context.Background(), receipt); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			store, err := mediaproduction.OpenPhotoCustody(filepath.Join(root, "photos"), 64)
			if err != nil {
				t.Fatal(err)
			}
			reference, err := store.AttachPhoto(context.Background(), mediaflow.PhotoAttachment{FileID: "file", FileUniqueID: "unique", MIMEType: "image/png", Content: []byte("original")})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, store, reference, filepath.Join(root, "photos", reference))
			path, err := store.ResolveAttachment(context.Background(), reference)
			if path != "" || !errors.Is(err, mediaproduction.ErrPhotoUnavailable) {
				t.Fatalf("ResolveAttachment() = %q, %v", path, err)
			}
			if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), reference) {
				t.Fatalf("unsafe error text %q", err)
			}
		})
	}
}

func TestPhotoCustodyResolveAttachmentRejectsInvalidReferenceAndCanceledContext(t *testing.T) {
	store, err := mediaproduction.OpenPhotoCustody(filepath.Join(canonicalTempDir(t), "photos"), 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{"", "../0123456789abcdef0123456789abcdef", "/tmp/photo", strings.Repeat("0", 31), strings.Repeat("g", 32)} {
		if path, err := store.ResolveAttachment(context.Background(), reference); path != "" || !errors.Is(err, mediaproduction.ErrPhotoUnavailable) {
			t.Fatalf("ResolveAttachment(%q) = %q, %v", reference, path, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if path, err := store.ResolveAttachment(ctx, strings.Repeat("0", 32)); path != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveAttachment(canceled) = %q, %v", path, err)
	}
}

func TestPhotoCustodyRejectsTrailingMetadataInsteadOfAcceptingFirstJSONValue(t *testing.T) {
	directory := filepath.Join(canonicalTempDir(t), "photos")
	store, err := mediaproduction.OpenPhotoCustody(directory, 64)
	if err != nil {
		t.Fatalf("OpenPhotoCustody() error = %v", err)
	}
	content := []byte("original")
	reference, err := store.AttachPhoto(context.Background(), mediaflow.PhotoAttachment{
		FileID: "file", FileUniqueID: "unique", MIMEType: "image/png", Content: content,
	})
	if err != nil {
		t.Fatalf("AttachPhoto() error = %v", err)
	}
	metadataPath := filepath.Join(directory, reference, "custody.json")
	handle, err := os.OpenFile(metadataPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	if _, err := handle.WriteString("{}"); err != nil {
		_ = handle.Close()
		t.Fatalf("append metadata: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close metadata: %v", err)
	}
	digest := sha256.Sum256(content)
	err = store.VerifyPhoto(context.Background(), reference, mediaflow.PhotoDigest{Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])})
	if !errors.Is(err, mediaproduction.ErrPhotoCorrupt) {
		t.Fatalf("VerifyPhoto(trailing metadata) error = %v", err)
	}
}

func TestOpenPhotoCustodyRemovesOnlyOwnedStaleTemporaryDirectories(t *testing.T) {
	root := canonicalTempDir(t)
	directory := filepath.Join(root, "photos")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create custody directory: %v", err)
	}
	stale := filepath.Join(directory, ".photo-stale")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatalf("create stale temporary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "photo"), []byte("partial photo"), 0o600); err != nil {
		t.Fatalf("write stale temporary: %v", err)
	}
	if _, err := mediaproduction.OpenPhotoCustody(directory, 64); err != nil {
		t.Fatalf("OpenPhotoCustody() error = %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale custody temporary remains: %v", err)
	}
}

func TestOpenRejectsSymlinkDirectoryWithoutMutatingItsTarget(t *testing.T) {
	root := canonicalTempDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("create target: %v", err)
	}
	link := filepath.Join(root, "photos-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if _, err := mediaproduction.OpenPhotoCustody(link, 64); !errors.Is(err, mediaproduction.ErrInvalidConfiguration) {
		t.Fatalf("OpenPhotoCustody(symlink) error = %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode mutated to %o", info.Mode().Perm())
	}
}

func TestOpenRejectsSymlinkCustodyLock(t *testing.T) {
	root := canonicalTempDir(t)
	directory := filepath.Join(root, "photos")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create custody directory: %v", err)
	}
	target := filepath.Join(root, "lock-target")
	if err := os.WriteFile(target, []byte("unrelated"), 0o600); err != nil {
		t.Fatalf("create lock target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(directory, ".custody.lock")); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if _, err := mediaproduction.OpenPhotoCustody(directory, 64); !errors.Is(err, mediaproduction.ErrInvalidConfiguration) {
		t.Fatalf("OpenPhotoCustody(symlink lock) error = %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "unrelated" {
		t.Fatalf("lock target changed: %q, %v", content, err)
	}
}

func TestRuntimePhotoUsesDurableCustodyAndNeverDownloadsVideo(t *testing.T) {
	var mu sync.Mutex
	requests := make([]telegram.DownloadMediaRequest, 0, 1)
	downloader := downloaderFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		return telegram.DownloadedMedia{
			File:    telegram.File{FileID: request.FileID, FileUniqueID: "photo-unique", FileSize: 5, FilePath: "p.jpg"},
			Content: []byte("photo"),
		}, nil
	})
	runtime := openTestRuntime(t, downloader, mediaproduction.DocumentsReject, nil)
	reference, err := runtime.Preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "photo", FileID: "photo-file", FileUniqueID: "photo-unique", FileSize: 5,
		MIMEType: "image/jpeg", DownloadPermitted: true,
	})
	if err != nil {
		t.Fatalf("Prepare(photo) error = %v", err)
	}
	if len(reference) != 32 || strings.ContainsAny(reference, `/\\`) || filepath.IsAbs(reference) {
		t.Fatalf("durable opaque photo reference: %q", reference)
	}
	if _, err := runtime.Preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "video", FileID: "video-file", FileUniqueID: "video-unique", FileSize: 5,
		DownloadPermitted: true,
	}); !errors.Is(err, mediaflow.ErrDownloadForbidden) {
		t.Fatalf("Prepare(video) error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0].Kind != telegram.MediaPhoto || requests[0].MaxBytes != 64 {
		t.Fatalf("download requests = %#v", requests)
	}
}

func TestRuntimeNeverDownloadsUnknownMedia(t *testing.T) {
	downloader := downloaderFunc(func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		t.Fatal("unknown media used Telegram downloader")
		return telegram.DownloadedMedia{}, nil
	})
	runtime := openTestRuntime(t, downloader, mediaproduction.DocumentsReject, nil)
	if _, err := runtime.Preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{
		Kind: "animation", FileID: "animation-file", FileSize: 5, DownloadPermitted: true,
	}); !errors.Is(err, mediaflow.ErrUnsupportedMedia) {
		t.Fatalf("Prepare(unknown) error = %v", err)
	}
}

func TestPhotoCustodyCreatesIndependentDurableReferencesConcurrently(t *testing.T) {
	store, err := mediaproduction.OpenPhotoCustody(filepath.Join(canonicalTempDir(t), "photos"), 64)
	if err != nil {
		t.Fatalf("OpenPhotoCustody() error = %v", err)
	}
	const count = 24
	references := make(chan string, count)
	errorsSeen := make(chan error, count)
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			content := []byte(fmt.Sprintf("photo-%02d", index))
			reference, err := store.AttachPhoto(context.Background(), mediaflow.PhotoAttachment{
				FileID: fmt.Sprintf("file-%02d", index), FileUniqueID: fmt.Sprintf("unique-%02d", index),
				MIMEType: "image/webp", Content: content,
			})
			if err != nil {
				errorsSeen <- err
				return
			}
			digest := sha256.Sum256(content)
			if err := store.VerifyPhoto(context.Background(), reference, mediaflow.PhotoDigest{
				Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]),
			}); err != nil {
				errorsSeen <- err
				return
			}
			references <- reference
		}(index)
	}
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent custody error = %v", err)
	}
	close(references)
	seen := make(map[string]struct{}, count)
	for reference := range references {
		seen[reference] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("unique durable references = %d, want %d", len(seen), count)
	}
}

func TestRuntimeRequiresExplicitDocumentPolicy(t *testing.T) {
	downloader := downloaderFunc(func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		t.Fatal("document used Telegram downloader")
		return telegram.DownloadedMedia{}, nil
	})
	if _, err := openRuntime(t, downloader, "", nil); !errors.Is(err, mediaproduction.ErrInvalidConfiguration) {
		t.Fatalf("Open(no document mode) error = %v", err)
	}
	if _, err := openRuntime(t, downloader, mediaproduction.DocumentsPrepare, nil); !errors.Is(err, mediaproduction.ErrInvalidConfiguration) {
		t.Fatalf("Open(custom without policy) error = %v", err)
	}
	policy := documentPolicyFunc(func(_ context.Context, input telegramcontroller.IncomingInput) (string, error) {
		return "document:" + input.FileID, nil
	})
	runtime := openTestRuntime(t, downloader, mediaproduction.DocumentsPrepare, policy)
	prepared, err := runtime.Preparer.Prepare(context.Background(), telegramcontroller.IncomingInput{Kind: "document", FileID: "doc", FileSize: 4})
	if err != nil || prepared != "document:doc" {
		t.Fatalf("Prepare(document) = %q, %v", prepared, err)
	}
}

func openTestRuntime(t *testing.T, downloader downloaderFunc, mode mediaproduction.DocumentMode, policy mediaflow.DocumentPolicy) *mediaproduction.Runtime {
	t.Helper()
	runtime, err := openRuntime(t, downloader, mode, policy)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return runtime
}

func openRuntime(t *testing.T, downloader downloaderFunc, mode mediaproduction.DocumentMode, policy mediaflow.DocumentPolicy) (*mediaproduction.Runtime, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	return mediaproduction.Open(downloader, mediaproduction.Config{
		VoiceTempDirectory: filepath.Join(canonicalTempDir(t), "voice"),
		PhotoDirectory:     filepath.Join(canonicalTempDir(t), "photos"),
		VoiceBytes:         32, PhotoBytes: 64, PreparedBytes: 1024,
		Parakeet:     parakeet.Command{Executable: executable, ModelPath: regularModelFile(t), Arguments: []string{parakeet.ModelPathPlaceholder}, Environment: []string{}, MaxTranscriptBytes: 64, MaxDiagnosticBytes: 64},
		DocumentMode: mode, DocumentPolicy: policy,
	})
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return resolved
}

func regularModelFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(canonicalTempDir(t), "model")
	if err := os.WriteFile(path, []byte("model"), 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	return path
}
