package telegram_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"bria/internal/telegram"
)

func TestRichPhotoFileIDExtractsLargestNestedReceiptPhoto(t *testing.T) {
	var message telegram.Message
	if err := json.Unmarshal([]byte(`{
		"message_id":81,
		"chat":{"id":42,"type":"private"},
		"rich_message":{"media":[
			{"file_id":"small-photo","width":80,"height":40,"file_size":100},
			{"file_id":"large-photo","width":800,"height":400,"file_size":1000}
		]}
	}`), &message); err != nil {
		t.Fatal(err)
	}
	if got := telegram.RichPhotoFileID(message); got != "large-photo" {
		t.Fatalf("RichPhotoFileID() = %q, want largest photo", got)
	}
}

func TestRichPhotoCanReuseConfirmedTelegramFileIDWithoutMultipartUpload(t *testing.T) {
	const token = "654399:test-only"
	calls := 0
	client := mustTestClient(t, token, httpClientFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("content type = %q, want JSON file_id reuse", got)
		}
		var body telegram.SendRichMessageRequest
		decodeExactRequest(t, request, &body)
		if len(body.PhotoPNG) != 0 || len(body.RichMessage.Media) != 1 || body.RichMessage.Media[0].Media.Media != "telegram-confirmed-file-id" {
			t.Fatalf("rich reuse request = %#v", body)
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":81,"from":{"id":600,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"}}}`), nil
	}), telegram.Options{})

	rich := telegram.InputRichMessage{
		Markdown: "text\n\n![](tg://photo?id=" + telegram.ScreenPhotoID + ")",
		Media: []telegram.InputRichMessageMedia{{
			ID:    telegram.ScreenPhotoID,
			Media: telegram.InputRichMediaPhoto{Type: "photo", Media: "telegram-confirmed-file-id"},
		}},
	}
	message, err := client.SendRichMessage(context.Background(), telegram.SendRichMessageRequest{ChatID: 42, RichMessage: rich})
	if err != nil || message.MessageID != 81 || calls != 1 {
		t.Fatalf("send rich reuse = %#v, calls=%d, err=%v", message, calls, err)
	}
}

func TestRichPhotoRejectsMissingUploadAndOversizedPNGBeforeTransport(t *testing.T) {
	calls := 0
	client := mustTestClient(t, "654398:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	}), telegram.Options{})
	rich := telegram.InputRichMessage{
		Markdown: "text\n\n![](tg://photo?id=" + telegram.ScreenPhotoID + ")",
		Media: []telegram.InputRichMessageMedia{{
			ID:    telegram.ScreenPhotoID,
			Media: telegram.InputRichMediaPhoto{Type: "photo", Media: "attach://" + telegram.ScreenPhotoID},
		}},
	}
	if _, err := client.SendRichMessage(context.Background(), telegram.SendRichMessageRequest{ChatID: 42, RichMessage: rich}); err == nil {
		t.Fatal("missing multipart upload was accepted")
	}
	if _, err := client.SendRichMessage(context.Background(), telegram.SendRichMessageRequest{
		ChatID: 42, RichMessage: rich, PhotoPNG: make([]byte, (1<<20)+1),
	}); err == nil {
		t.Fatal("oversized rich PNG was accepted")
	}
	if calls != 0 {
		t.Fatalf("invalid rich photo reached transport %d times", calls)
	}
}

func TestRichPhotoFileIDValidationRejectsControlCharacters(t *testing.T) {
	client := mustTestClient(t, "654397:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid file id reached transport")
		return nil, nil
	}), telegram.Options{})
	rich := telegram.InputRichMessage{
		Markdown: "![](tg://photo?id=" + telegram.ScreenPhotoID + ")",
		Media: []telegram.InputRichMessageMedia{{
			ID:    telegram.ScreenPhotoID,
			Media: telegram.InputRichMediaPhoto{Type: "photo", Media: "bad file id"},
		}},
	}
	if _, err := client.SendRichMessage(context.Background(), telegram.SendRichMessageRequest{ChatID: 42, RichMessage: rich}); err == nil {
		t.Fatal("unsafe Telegram file id was accepted")
	}
}
