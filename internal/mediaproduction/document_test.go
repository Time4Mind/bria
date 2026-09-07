package mediaproduction

import (
	"context"
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

type photoAttacherFunc func(context.Context, mediaflow.PhotoAttachment) (string, error)

func (f photoAttacherFunc) AttachPhoto(ctx context.Context, a mediaflow.PhotoAttachment) (string, error) {
	return f(ctx, a)
}
