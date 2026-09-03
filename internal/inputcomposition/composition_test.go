package inputcomposition_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/inputcomposition"
	"bria/internal/mediaproduction"
	"bria/internal/telegram"
	"bria/internal/turnprocessing"
)

type downloaderFunc func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error)

func (function downloaderFunc) DownloadMedia(ctx context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
	return function(ctx, request)
}

func TestProductionPhotoCompositionProducesOpaqueExactCustodyReference(t *testing.T) {
	content := []byte("photo bytes")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	voice, photos := filepath.Join(root, "voice"), filepath.Join(root, "photos")
	if err := os.Mkdir(voice, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(photos, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := mediaproduction.Open(downloaderFunc(func(_ context.Context, request telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		return telegram.DownloadedMedia{File: telegram.File{FileID: request.FileID, FileUniqueID: "photo-unique", FileSize: int64(len(content))}, Content: content}, nil
	}), mediaproduction.Config{
		VoiceTempDirectory: voice, PhotoDirectory: photos,
		VoiceBytes: 32, PhotoBytes: 64, PreparedBytes: 1024, DocumentMode: mediaproduction.DocumentsReject,
	})
	if err != nil {
		t.Fatal(err)
	}
	composition, err := inputcomposition.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := composition.PrepareStructured(context.Background(), turnprocessing.IncomingInput{
		Kind: "photo", FileID: "photo-file", FileUniqueID: "photo-unique", FileSize: int64(len(content)), MIMEType: "image/jpeg", DownloadPermitted: true,
	})
	if err != nil {
		t.Fatalf("PrepareStructured() error = %v", err)
	}
	if prepared.Text != "" || len(prepared.Attachments) != 1 || strings.ContainsAny(prepared.Attachments[0].Reference, `/\\`) {
		t.Fatalf("prepared input = %#v", prepared)
	}
	digest := sha256.Sum256(content)
	attachment := prepared.Attachments[0]
	if attachment.Size != int64(len(content)) || attachment.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("attachment = %#v", attachment)
	}
	receipt := turnprocessing.AttachmentReceipt{Reference: attachment.Reference, ProviderSession: "provider-1", MessageID: "message-1"}
	if err := composition.MarkAccepted(context.Background(), receipt); err != nil {
		t.Fatalf("MarkAccepted() error = %v", err)
	}
	if err := composition.MarkCompleted(context.Background(), receipt); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	if _, err := composition.Prepare(context.Background(), turnprocessing.IncomingInput{Kind: "photo", FileID: "second", FileUniqueID: "photo-unique", FileSize: int64(len(content)), MIMEType: "image/jpeg", DownloadPermitted: true}); err != inputcomposition.ErrStructuredAttachment {
		t.Fatalf("Prepare(photo) error = %v", err)
	}
}
