package documentproduction

import (
	"context"
	"errors"
	"testing"

	"bria/internal/mediaflow"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

type documentDownloaderFunc func(context.Context, telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error)

func (f documentDownloaderFunc) DownloadMedia(ctx context.Context, req telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
	return f(ctx, req)
}

func TestTextDocumentPolicyAcceptsBoundedMarkdown(t *testing.T) {
	policy := TextDocumentPolicy{MaxBytes: 64, Downloader: documentDownloaderFunc(func(_ context.Context, req telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		if req.Kind != telegram.MediaDocument || req.MaxBytes != 64 {
			t.Fatalf("download request = %#v", req)
		}
		return telegram.DownloadedMedia{File: telegram.File{FileID: "f", FileUniqueID: "u", FileSize: 11}, Content: []byte("# hello\nbye")}, nil
	})}
	policy.Attacher = photoAttacherFunc(func(context.Context, mediaflow.PhotoAttachment) (string, error) { return "ref", nil })
	got, err := policy.PrepareDocumentStructured(context.Background(), telegramcontroller.IncomingInput{Kind: "document", FileID: "f", FileUniqueID: "u", FileSize: 11, MIMEType: "text/markdown", DownloadPermitted: true})
	if err != nil || len(got.Attachments) != 1 || got.Text != "" {
		t.Fatalf("document = %#v, err=%v", got, err)
	}
}

func TestTextDocumentPolicyAcceptsMissingDeclaredSizeAfterBoundedDownload(t *testing.T) {
	content := []byte("forwarded document")
	policy := TextDocumentPolicy{MaxBytes: 64, Downloader: documentDownloaderFunc(func(_ context.Context, req telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		if req.Kind != telegram.MediaDocument || req.FileID != "f" || req.MaxBytes != 64 {
			t.Fatalf("download request = %#v", req)
		}
		return telegram.DownloadedMedia{File: telegram.File{FileID: "f", FileUniqueID: "u"}, Content: content}, nil
	})}
	policy.Attacher = photoAttacherFunc(func(_ context.Context, attachment mediaflow.PhotoAttachment) (string, error) {
		if string(attachment.Content) != string(content) {
			t.Fatalf("attached content = %q", attachment.Content)
		}
		return "ref", nil
	})

	got, err := policy.PrepareDocumentStructured(context.Background(), telegramcontroller.IncomingInput{
		Kind: "document", FileID: "f", FileUniqueID: "u", FileSize: 0, DownloadPermitted: true,
	})
	if err != nil {
		t.Fatalf("PrepareDocumentStructured() error = %v", err)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Reference != "ref" || got.Attachments[0].Size != int64(len(content)) {
		t.Fatalf("document = %#v", got)
	}
}

func TestTextDocumentPolicyRejectsActualContentAboveBoundWithMissingDeclaredSize(t *testing.T) {
	downloaded := false
	policy := TextDocumentPolicy{MaxBytes: 3, Downloader: documentDownloaderFunc(func(_ context.Context, req telegram.DownloadMediaRequest) (telegram.DownloadedMedia, error) {
		downloaded = true
		if req.MaxBytes != 3 {
			t.Fatalf("download bound = %d, want 3", req.MaxBytes)
		}
		return telegram.DownloadedMedia{File: telegram.File{FileID: "f", FileUniqueID: "u"}, Content: []byte("four")}, nil
	})}
	policy.Attacher = photoAttacherFunc(func(context.Context, mediaflow.PhotoAttachment) (string, error) {
		t.Fatal("oversized document reached attachment custody")
		return "", nil
	})

	_, err := policy.PrepareDocumentStructured(context.Background(), telegramcontroller.IncomingInput{
		Kind: "document", FileID: "f", FileUniqueID: "u", FileSize: 0, DownloadPermitted: true,
	})
	if !errors.Is(err, mediaflow.ErrMediaTooLarge) {
		t.Fatalf("PrepareDocumentStructured() error = %v, want ErrMediaTooLarge", err)
	}
	if !downloaded {
		t.Fatal("missing declared size was rejected before bounded download")
	}
}

type photoAttacherFunc func(context.Context, mediaflow.PhotoAttachment) (string, error)

func (f photoAttacherFunc) AttachPhoto(ctx context.Context, a mediaflow.PhotoAttachment) (string, error) {
	return f(ctx, a)
}
