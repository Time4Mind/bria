package telegrambridge

import (
	"testing"

	"bria/internal/telegram"
)

func TestNormalizeUpdatePreservesDocumentMetadata(t *testing.T) {
	got := normalizeUpdate(telegram.Update{UpdateID: 7, Message: &telegram.Message{
		MessageID: 9, From: &telegram.User{ID: 1}, Chat: telegram.Chat{ID: 1, Type: "private"},
		Caption: "context", Document: &telegram.Document{FileID: "f", FileUniqueID: "u", FileSize: 12, MIMEType: "text/markdown"},
	}})
	if got.MediaKind != "document" || got.MediaFileID != "f" || got.MediaFileUniqueID != "u" || got.MediaFileSize != 12 || !got.MediaDownloadAllowed {
		t.Fatalf("normalized document = %#v", got)
	}
}
