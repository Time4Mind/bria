package telegram_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bria/internal/telegram"
)

func TestClientGetUpdatesLongPoll(t *testing.T) {
	const testToken = "123456:test-only"
	httpClient := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if got, want := request.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := request.URL.Path, "/bot"+testToken+"/getUpdates"; got != want {
			t.Errorf("request path mismatch")
		}
		if got, want := request.Header.Get("Content-Type"), "application/json"; got != want {
			t.Errorf("Content-Type = %q, want %q", got, want)
		}

		var body struct {
			Offset         telegram.UpdateID `json:"offset"`
			Limit          int               `json:"limit"`
			TimeoutSeconds int               `json:"timeout"`
			AllowedUpdates []string          `json:"allowed_updates"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatalf("decode getUpdates request: %v", err)
		}
		if got, want := body.Offset, telegram.UpdateID(41); got != want {
			t.Errorf("offset = %d, want %d", got, want)
		}
		if got, want := body.Limit, 25; got != want {
			t.Errorf("limit = %d, want %d", got, want)
		}
		if got, want := body.TimeoutSeconds, 30; got != want {
			t.Errorf("timeout = %d, want %d", got, want)
		}
		if got, want := len(body.AllowedUpdates), 2; got != want {
			t.Errorf("allowed_updates count = %d, want %d", got, want)
		}

		return jsonResponse(http.StatusOK, `{
  "ok": true,
  "future_envelope_field": "ignored",
  "result": [
    {
      "update_id": 42,
      "future_update_field": {"ignored": true},
      "message": {
        "message_id": 101,
        "from": {"id": 7000000001, "is_bot": false, "first_name": "Artem", "future_user_field": 1},
        "chat": {"id": 8000000002, "type": "private", "future_chat_field": 1},
        "text": "/new"
      }
    },
    {
      "update_id": 43,
      "callback_query": {
        "id": "opaque-callback-43",
        "from": {"id": 7000000001, "is_bot": false, "first_name": "Artem"},
        "message": {
          "message_id": 102,
          "from": {"id": 6000000001, "is_bot": true, "first_name": "Bria"},
          "chat": {"id": 8000000002, "type": "private"},
          "text": "Confirm"
        },
        "data": "confirm:create"
      }
    }
  ]
}`), nil
	})

	client, err := telegram.NewClient(testToken, httpClient, telegram.Options{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	updates, err := client.GetUpdates(context.Background(), telegram.GetUpdatesRequest{
		Offset:         41,
		Limit:          25,
		TimeoutSeconds: 30,
		AllowedUpdates: []string{"message", "callback_query"},
	})
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if got, want := len(updates), 2; got != want {
		t.Fatalf("updates count = %d, want %d", got, want)
	}
	if got, want := updates[0].UpdateID, telegram.UpdateID(42); got != want {
		t.Errorf("message update id = %d, want %d", got, want)
	}
	if updates[0].Message == nil || updates[0].Message.From == nil {
		t.Fatal("message update is missing Message.From")
	}
	if got, want := updates[0].Message.MessageID, telegram.MessageID(101); got != want {
		t.Errorf("message id = %d, want %d", got, want)
	}
	if got, want := updates[0].Message.From.ID, telegram.UserID(7000000001); got != want {
		t.Errorf("user id = %d, want %d", got, want)
	}
	if got, want := updates[0].Message.Chat.ID, telegram.ChatID(8000000002); got != want {
		t.Errorf("chat id = %d, want %d", got, want)
	}
	if updates[1].CallbackQuery == nil || updates[1].CallbackQuery.Message == nil {
		t.Fatal("callback update is missing CallbackQuery.Message")
	}
	if got, want := updates[1].CallbackQuery.ID, telegram.CallbackQueryID("opaque-callback-43"); got != want {
		t.Errorf("callback query id = %q, want %q", got, want)
	}
	if got, want := updates[1].CallbackQuery.Message.MessageID, telegram.MessageID(102); got != want {
		t.Errorf("callback message id = %d, want %d", got, want)
	}
}

func TestClientGetUpdatesDecodesReplyCaptionAndTypedMedia(t *testing.T) {
	t.Parallel()

	client := mustTestClient(t, "123456:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"ok":true,"result":[
  {"update_id":101,"message":{"message_id":201,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"voice note","reply_to_message":{"message_id":190},"voice":{"file_id":"voice-file","file_unique_id":"voice-unique","duration":7,"mime_type":"audio/ogg","file_size":4096}}},
  {"update_id":102,"message":{"message_id":202,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"photo note","photo":[{"file_id":"small","file_unique_id":"small-u","width":90,"height":90,"file_size":100},{"file_id":"large","file_unique_id":"large-u","width":1280,"height":720,"file_size":9000}]}},
  {"update_id":103,"message":{"message_id":203,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"video caption only","video":{"file_id":"video-file","file_unique_id":"video-unique","width":1920,"height":1080,"duration":12,"mime_type":"video/mp4","file_size":2000000}}},
  {"update_id":104,"message":{"message_id":204,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"document caption","document":{"file_id":"ignored-by-this-boundary"}}}
]}`), nil
	}), telegram.Options{})

	updates, err := client.GetUpdates(context.Background(), telegram.GetUpdatesRequest{Limit: 10})
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if len(updates) != 4 {
		t.Fatalf("updates count = %d, want 4", len(updates))
	}

	voice := updates[0].Message
	if voice == nil || voice.ReplyToMessage == nil || voice.ReplyToMessage.MessageID != 190 {
		t.Fatalf("voice reply metadata = %#v, want message 190", voice)
	}
	if voice.Caption != "voice note" || voice.Voice == nil ||
		voice.Voice.FileID != "voice-file" || voice.Voice.FileUniqueID != "voice-unique" ||
		voice.Voice.Duration != 7 || voice.Voice.MIMEType != "audio/ogg" || voice.Voice.FileSize != 4096 {
		t.Fatalf("voice message = %#v, want complete typed metadata", voice)
	}

	photo := updates[1].Message
	if photo == nil || photo.Caption != "photo note" || len(photo.Photo) != 2 {
		t.Fatalf("photo message = %#v, want caption and two sizes", photo)
	}
	if got := photo.Photo[1]; got.FileID != "large" || got.FileUniqueID != "large-u" ||
		got.Width != 1280 || got.Height != 720 || got.FileSize != 9000 {
		t.Fatalf("largest photo DTO = %#v, want complete typed metadata", got)
	}

	video := updates[2].Message
	if video == nil || video.Caption != "video caption only" || video.Video == nil ||
		video.Video.FileID != "video-file" || video.Video.FileUniqueID != "video-unique" ||
		video.Video.Width != 1920 || video.Video.Height != 1080 || video.Video.Duration != 12 ||
		video.Video.MIMEType != "video/mp4" || video.Video.FileSize != 2000000 {
		t.Fatalf("video message = %#v, want metadata without a download", video)
	}

	document := updates[3].Message
	if document == nil || document.Caption != "document caption" || document.Voice != nil ||
		document.Video != nil || len(document.Photo) != 0 {
		t.Fatalf("other captioned message = %#v, want caption without downloadable media", document)
	}
}

func TestClientAlwaysTargetsOfficialTelegramAPI(t *testing.T) {
	const testToken = "101010:test-only"
	var observed *http.Request
	httpClient := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		observed = request
		return jsonResponse(http.StatusOK, `{"ok":true,"result":[]}`), nil
	})
	client, err := telegram.NewClient(testToken, httpClient, telegram.Options{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.GetUpdates(context.Background(), validGetUpdatesRequest()); err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if observed == nil {
		t.Fatal("HTTP client observed no request")
	}
	if got, want := observed.URL.Scheme, "https"; got != want {
		t.Errorf("request scheme = %q, want %q", got, want)
	}
	if got, want := observed.URL.Host, "api.telegram.org"; got != want {
		t.Errorf("request host = %q, want %q", got, want)
	}
	if got, want := observed.URL.Path, "/bot"+testToken+"/getUpdates"; got != want {
		t.Errorf("request path mismatch")
	}
	if observed.URL.User != nil || observed.URL.RawQuery != "" || observed.URL.Fragment != "" {
		t.Errorf("official request URL has user info, query, or fragment")
	}
}

func TestClientGetMeReturnsVerifiedBotIdentity(t *testing.T) {
	const testToken = "202020:test-only"
	httpClient := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if got, want := request.URL.Path, "/bot"+testToken+"/getMe"; got != want {
			t.Errorf("request path = %q, want %q", got, want)
		}
		var body map[string]any
		decodeExactRequest(t, request, &body)
		if len(body) != 0 {
			t.Fatalf("getMe body = %#v, want empty object", body)
		}
		return jsonResponse(http.StatusOK, `{
  "ok": true,
  "result": {
    "id": 6000000001,
    "is_bot": true,
    "first_name": "Bria",
    "username": "bria_bot",
    "future_identity_field": true
  }
}`), nil
	})

	client := mustTestClient(t, testToken, httpClient, telegram.Options{})
	identity, err := client.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe() error = %v", err)
	}
	if identity.ID != 6000000001 || !identity.IsBot || identity.Username != "bria_bot" {
		t.Fatalf("GetMe() = %#v, want verified bot identity", identity)
	}
}

func TestClientDownloadsOnlyBoundedVoiceOrPhotoFromOfficialFileEndpoint(t *testing.T) {
	t.Parallel()

	const testToken = "202021:test-only"
	calls := 0
	client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/bot"+testToken+"/getFile" {
				t.Fatalf("getFile request = %s %s", request.Method, request.URL.Path)
			}
			var body telegram.GetFileRequest
			decodeExactRequest(t, request, &body)
			if body.FileID != "voice-file" {
				t.Fatalf("getFile file_id = %q, want voice-file", body.FileID)
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"result":{"file_id":"voice-file","file_unique_id":"voice-unique","file_size":5,"file_path":"voice/messages/file.oga"}}`), nil
		case 2:
			if request.Method != http.MethodGet {
				t.Fatalf("download method = %q, want GET", request.Method)
			}
			if request.URL.Scheme != "https" || request.URL.Host != "api.telegram.org" ||
				request.URL.Path != "/file/bot"+testToken+"/voice/messages/file.oga" ||
				request.URL.User != nil || request.URL.RawQuery != "" || request.URL.Fragment != "" {
				t.Fatalf("download URL shape is not the exact official file endpoint")
			}
			response := jsonResponse(http.StatusOK, "voice")
			response.Header.Set("Content-Length", "5")
			return response, nil
		default:
			t.Fatalf("unexpected HTTP call %d", calls)
			return nil, nil
		}
	}), telegram.Options{MaxDownloadBytes: 1024})

	download, err := client.DownloadMedia(context.Background(), telegram.DownloadMediaRequest{
		Kind: telegram.MediaVoice, FileID: "voice-file", MaxBytes: 16,
	})
	if err != nil {
		t.Fatalf("DownloadMedia() error = %v", err)
	}
	if calls != 2 || download.File.FileID != "voice-file" || download.File.FileUniqueID != "voice-unique" ||
		download.File.FileSize != 5 || download.File.FilePath != "voice/messages/file.oga" ||
		string(download.Content) != "voice" {
		t.Fatalf("DownloadMedia() = %#v after %d calls", download, calls)
	}
}

func TestClientMediaDownloadFailsClosedBeforeUnsafeOrOversizedFetch(t *testing.T) {
	t.Parallel()

	t.Run("video is never downloadable", func(t *testing.T) {
		calls := 0
		client := mustTestClient(t, "202022:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("must not be called")
		}), telegram.Options{})
		_, err := client.DownloadMedia(context.Background(), telegram.DownloadMediaRequest{
			Kind: telegram.MediaVideo, FileID: "video-file", MaxBytes: 1024,
		})
		if !errors.Is(err, telegram.ErrVideoDownloadForbidden) || calls != 0 {
			t.Fatalf("video download = (%v, %d calls), want explicit local rejection", err, calls)
		}
	})

	tests := []struct {
		name       string
		fileResult string
		maxBytes   int64
		body       string
		wantCalls  int
	}{
		{
			name:       "declared file is oversized",
			fileResult: `{"file_id":"photo","file_unique_id":"photo-u","file_size":11,"file_path":"photos/file.jpg"}`,
			maxBytes:   10,
			wantCalls:  1,
		},
		{
			name:       "unsafe traversal path",
			fileResult: `{"file_id":"photo","file_unique_id":"photo-u","file_size":5,"file_path":"photos/../secret"}`,
			maxBytes:   10,
			wantCalls:  1,
		},
		{
			name:       "actual body is oversized",
			fileResult: `{"file_id":"photo","file_unique_id":"photo-u","file_size":0,"file_path":"photos/file.jpg"}`,
			maxBytes:   5,
			body:       "123456",
			wantCalls:  2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := mustTestClient(t, "202023:test-only", httpClientFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return jsonResponse(http.StatusOK, `{"ok":true,"result":`+test.fileResult+`}`), nil
				}
				return jsonResponse(http.StatusOK, test.body), nil
			}), telegram.Options{MaxDownloadBytes: 100})
			_, err := client.DownloadMedia(context.Background(), telegram.DownloadMediaRequest{
				Kind: telegram.MediaPhoto, FileID: "photo", MaxBytes: test.maxBytes,
			})
			if err == nil {
				t.Fatal("DownloadMedia() error = nil, want fail-closed rejection")
			}
			if calls != test.wantCalls {
				t.Fatalf("HTTP calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestClientCreationUIMethods(t *testing.T) {
	const testToken = "654321:test-only"
	httpClient := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if got, want := request.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}

		switch request.URL.Path {
		case "/bot" + testToken + "/sendMessage":
			var body telegram.SendMessageRequest
			decodeExactRequest(t, request, &body)
			if got, want := body.ChatID, telegram.ChatID(8000000002); got != want {
				t.Errorf("send chat id = %d, want %d", got, want)
			}
			if got, want := body.Text, "Creating Codex session"; got != want {
				t.Errorf("send text = %q, want %q", got, want)
			}
			assertTestInlineKeyboard(t, body.ReplyMarkup)
			return jsonResponse(http.StatusOK, `{
	  "ok": true,
	  "result": {
    "message_id": 201,
    "from": {"id": 6000000001, "is_bot": true, "first_name": "Bria"},
    "chat": {"id": 8000000002, "type": "private"},
	    "text": "Creating Codex session",
	    "reply_markup": {"inline_keyboard":[[{"text":"Previous","callback_data":"page:previous"}]]},
	    "future_message_field": "ignored"
	  }
	}`), nil
		case "/bot" + testToken + "/editMessageText":
			var body telegram.EditMessageTextRequest
			decodeExactRequest(t, request, &body)
			if got, want := body.ChatID, telegram.ChatID(8000000002); got != want {
				t.Errorf("edit chat id = %d, want %d", got, want)
			}
			if got, want := body.MessageID, telegram.MessageID(201); got != want {
				t.Errorf("edit message id = %d, want %d", got, want)
			}
			if got, want := body.Text, "Codex session ready"; got != want {
				t.Errorf("edit text = %q, want %q", got, want)
			}
			assertTestInlineKeyboard(t, body.ReplyMarkup)
			return jsonResponse(http.StatusOK, `{
	  "ok": true,
	  "result": {
    "message_id": 201,
    "from": {"id": 6000000001, "is_bot": true, "first_name": "Bria"},
    "chat": {"id": 8000000002, "type": "private"},
	    "text": "Codex session ready",
	    "reply_markup": {"inline_keyboard":[[{"text":"Previous","callback_data":"page:previous"}]]}
	  }
	}`), nil
		case "/bot" + testToken + "/answerCallbackQuery":
			var body telegram.AnswerCallbackQueryRequest
			decodeExactRequest(t, request, &body)
			if got, want := body.CallbackQueryID, telegram.CallbackQueryID("opaque-confirm-201"); got != want {
				t.Errorf("callback query id = %q, want %q", got, want)
			}
			if got, want := body.Text, "Started"; got != want {
				t.Errorf("callback answer text = %q, want %q", got, want)
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"result":true,"future":1}`), nil
		default:
			t.Errorf("unexpected request path")
			return jsonResponse(http.StatusNotFound, `{"ok":false,"error_code":404}`), nil
		}
	})

	client, err := telegram.NewClient(testToken, httpClient, telegram.Options{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	sent, err := client.SendMessage(context.Background(), telegram.SendMessageRequest{
		ChatID:      8000000002,
		Text:        "Creating Codex session",
		ReplyMarkup: testInlineKeyboard(),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if got, want := sent.MessageID, telegram.MessageID(201); got != want {
		t.Errorf("sent message id = %d, want %d", got, want)
	}
	assertTestInlineKeyboard(t, sent.ReplyMarkup)

	edited, err := client.EditMessageText(context.Background(), telegram.EditMessageTextRequest{
		ChatID:      8000000002,
		MessageID:   sent.MessageID,
		Text:        "Codex session ready",
		ReplyMarkup: testInlineKeyboard(),
	})
	if err != nil {
		t.Fatalf("EditMessageText() error = %v", err)
	}
	if got, want := edited.Text, "Codex session ready"; got != want {
		t.Errorf("edited text = %q, want %q", got, want)
	}
	assertTestInlineKeyboard(t, edited.ReplyMarkup)

	err = client.AnswerCallbackQuery(context.Background(), telegram.AnswerCallbackQueryRequest{
		CallbackQueryID: "opaque-confirm-201",
		Text:            "Started",
	})
	if err != nil {
		t.Fatalf("AnswerCallbackQuery() error = %v", err)
	}
}

func TestClientDeleteMessageProvidesSecretCleanupPrimitive(t *testing.T) {
	t.Parallel()

	const testToken = "654322:test-only"
	client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/bot"+testToken+"/deleteMessage" {
			t.Fatalf("delete request = %s %s", request.Method, request.URL.Path)
		}
		var body telegram.DeleteMessageRequest
		decodeExactRequest(t, request, &body)
		if body.ChatID != 42 || body.MessageID != 701 {
			t.Fatalf("delete request = %#v, want chat 42 message 701", body)
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":true}`), nil
	}), telegram.Options{})

	if err := client.DeleteMessage(context.Background(), telegram.DeleteMessageRequest{
		ChatID: 42, MessageID: 701,
	}); err != nil {
		t.Fatalf("DeleteMessage() error = %v", err)
	}
}

func TestClientDeleteMessageRejectsInvalidTargetWithoutHTTP(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTestClient(t, "654323:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusOK, `{"ok":true,"result":true}`), nil
	}), telegram.Options{})
	for _, request := range []telegram.DeleteMessageRequest{
		{MessageID: 1},
		{ChatID: 42},
		{ChatID: 42, MessageID: -1},
	} {
		if err := client.DeleteMessage(context.Background(), request); err == nil {
			t.Errorf("DeleteMessage(%#v) error = nil", request)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid deletes reached HTTP %d times, want 0", calls)
	}
}

func TestClientSendDocumentReturnsConfirmedFileReceipt(t *testing.T) {
	t.Parallel()

	const testToken = "654324:test-only"
	client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/bot"+testToken+"/sendDocument" {
			t.Fatalf("sendDocument request = %s %s", request.Method, request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024); err != nil {
			t.Fatalf("parse multipart request: %v", err)
		}
		if request.FormValue("chat_id") != "42" || request.FormValue("caption") != "audit result" {
			t.Fatalf("multipart fields = %#v", request.MultipartForm.Value)
		}
		file, header, err := request.FormFile("document")
		if err != nil {
			t.Fatalf("document form file: %v", err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if header.Filename != "report.txt" || string(content) != "verified artifact" {
			t.Fatalf("document = %q %q", header.Filename, content)
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":801,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"document":{"file_id":"remote-file","file_unique_id":"remote-unique","file_name":"report.txt","mime_type":"text/plain","file_size":17}}}`), nil
	}), telegram.Options{MaxUploadBytes: 1024})

	receipt, err := client.SendDocument(context.Background(), telegram.SendDocumentRequest{
		ChatID: 42, FileName: "report.txt", ContentType: "text/plain",
		Content: []byte("verified artifact"), Caption: "audit result",
	})
	if err != nil {
		t.Fatalf("SendDocument() error = %v", err)
	}
	want := telegram.FileReceipt{MessageID: 801, ChatID: 42, FileID: "remote-file", FileUniqueID: "remote-unique"}
	if receipt != want {
		t.Fatalf("SendDocument() = %#v, want %#v", receipt, want)
	}
}

func TestClientSendPhotoReturnsExactChatAndMessageReceipt(t *testing.T) {
	t.Parallel()
	const testToken = "654329:test-only"
	client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/bot"+testToken+"/sendPhoto" {
			t.Fatalf("sendPhoto request = %s %s", request.Method, request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024); err != nil {
			t.Fatalf("parse multipart request: %v", err)
		}
		if request.FormValue("chat_id") != "42" {
			t.Fatalf("multipart fields = %#v", request.MultipartForm.Value)
		}
		file, header, err := request.FormFile("photo")
		if err != nil {
			t.Fatalf("photo form file: %v", err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil || header.Filename != "screen.png" || string(content) != "PNG" {
			t.Fatalf("photo = %q %q %v", header.Filename, content, err)
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":802,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"photo":[{"file_id":"photo","file_unique_id":"photo-u","width":1,"height":1,"file_size":3}]}}`), nil
	}), telegram.Options{MaxUploadBytes: 1024})

	receipt, err := client.SendPhoto(context.Background(), telegram.SendPhotoRequest{ChatID: 42, FileName: "screen.png", ContentType: "image/png", Content: []byte("PNG")})
	if err != nil {
		t.Fatalf("SendPhoto() error = %v", err)
	}
	if want := (telegram.PhotoReceipt{MessageID: 802, ChatID: 42}); receipt != want {
		t.Fatalf("SendPhoto() = %#v, want %#v", receipt, want)
	}
}

func TestClientSendDocumentRejectsReceiptFromWrongChat(t *testing.T) {
	t.Parallel()
	client := mustTestClient(t, "654324:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":801,"from":{"id":600,"is_bot":true},"chat":{"id":43,"type":"private"},"document":{"file_id":"remote-file","file_unique_id":"remote-unique","file_name":"report.txt","file_size":7}}}`), nil
	}), telegram.Options{MaxUploadBytes: 1024})
	_, err := client.SendDocument(context.Background(), telegram.SendDocumentRequest{
		ChatID: 42, FileName: "report.txt", Content: []byte("content"),
	})
	if !errors.Is(err, telegram.ErrDeliveryUnknown) {
		t.Fatalf("SendDocument(wrong chat) error = %v", err)
	}
}

func TestClientSendDocumentClassifiesAmbiguousOutcomeWithoutRetryOrSecrets(t *testing.T) {
	t.Parallel()

	const testToken = "654325:redact-this"
	const documentSecret = "document-secret"
	calls := 0
	client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("dial %s with %s", request.URL.String(), documentSecret)
	}), telegram.Options{})

	_, err := client.SendDocument(context.Background(), telegram.SendDocumentRequest{
		ChatID: 42, FileName: "report.txt", Content: []byte(documentSecret),
	})
	if err == nil || !telegram.IsDeliveryUnknown(err) {
		t.Fatalf("SendDocument() error = %v, want unknown delivery", err)
	}
	if calls != 1 {
		t.Fatalf("sendDocument calls = %d, want exactly one", calls)
	}
	assertNoTransportSecret(t, err.Error(), testToken, documentSecret, "https://api.telegram.org")
}

func TestClientSendDocumentKeepsDefinitiveAPIRejectionDistinct(t *testing.T) {
	t.Parallel()

	client := mustTestClient(t, "654326:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"rejected"}`), nil
	}), telegram.Options{})
	_, err := client.SendDocument(context.Background(), telegram.SendDocumentRequest{
		ChatID: 42, FileName: "report.txt", Content: []byte("artifact"),
	})
	var apiErr *telegram.APIError
	if err == nil || !errors.As(err, &apiErr) || telegram.IsDeliveryUnknown(err) {
		t.Fatalf("SendDocument() error = %v, want definitive API rejection", err)
	}
}

func TestClientSendDocumentTreatsServerFailureAsAmbiguous(t *testing.T) {
	t.Parallel()

	client := mustTestClient(t, "654328:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"ok":false,"error_code":500,"description":"failed after request admission"}`), nil
	}), telegram.Options{})
	_, err := client.SendDocument(context.Background(), telegram.SendDocumentRequest{
		ChatID: 42, FileName: "report.txt", Content: []byte("artifact"),
	})
	if err == nil || !telegram.IsDeliveryUnknown(err) {
		t.Fatalf("SendDocument() error = %v, want unknown delivery after server failure", err)
	}
}

func TestClientSendDocumentRejectsUnsafeOrOversizedInputBeforeHTTP(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTestClient(t, "654327:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not be called")
	}), telegram.Options{MaxUploadBytes: 8})
	requests := []telegram.SendDocumentRequest{
		{FileName: "report.txt", Content: []byte("x")},
		{ChatID: 42, FileName: "../report.txt", Content: []byte("x")},
		{ChatID: 42, FileName: "report.txt"},
		{ChatID: 42, FileName: "report.txt", Content: []byte("123456789")},
	}
	for _, request := range requests {
		if _, err := client.SendDocument(context.Background(), request); err == nil {
			t.Errorf("SendDocument(%#v) error = nil", request)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid documents reached HTTP %d times, want 0", calls)
	}
}

func testInlineKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{
		Text: "Previous", CallbackData: "page:previous",
	}}}}
}

func assertTestInlineKeyboard(t *testing.T, markup *telegram.InlineKeyboardMarkup) {
	t.Helper()
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("inline keyboard = %#v, want one button", markup)
	}
	button := markup.InlineKeyboard[0][0]
	if button.Text != "Previous" || button.CallbackData != "page:previous" {
		t.Fatalf("inline button = %#v, want Previous/page:previous", button)
	}
}

func decodeExactRequest(t *testing.T, request *http.Request, target any) {
	t.Helper()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}

func TestClientRedactsAPIAndTransportErrors(t *testing.T) {
	const testToken = "987654:redact-this"
	httpClient := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(
			http.StatusBadRequest,
			fmt.Sprintf(
				`{"ok":false,"error_code":400,"description":"request %s failed for token %s"}`,
				request.URL.String(),
				testToken,
			),
		), nil
	})

	client, err := telegram.NewClient(testToken, httpClient, telegram.Options{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.SendMessage(context.Background(), telegram.SendMessageRequest{
		ChatID: 8000000002,
		Text:   "safe text",
	})
	if err == nil {
		t.Fatal("SendMessage() error = nil, want API rejection")
	}
	assertNoTransportSecret(t, err.Error(), testToken, "https://api.telegram.org")
	var apiError *telegram.APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("SendMessage() error = %T, want *telegram.APIError", err)
	}
	if apiError.HTTPStatus != http.StatusBadRequest || apiError.ErrorCode != 400 {
		t.Errorf("API error = %#v, want HTTP 400 and Telegram 400", apiError)
	}
	assertNoTransportSecret(t, fmt.Sprintf("%v|%+v|%#v", client, client, client), testToken, "https://api.telegram.org")

	transportClient, err := telegram.NewClient(
		testToken,
		httpClientFunc(func(request *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial %s using %s", request.URL.String(), testToken)
		}),
		telegram.Options{},
	)
	if err != nil {
		t.Fatalf("NewClient() for transport error = %v", err)
	}
	_, err = transportClient.SendMessage(context.Background(), telegram.SendMessageRequest{
		ChatID: 8000000002,
		Text:   "safe text",
	})
	if err == nil {
		t.Fatal("SendMessage() transport error = nil")
	}
	assertNoTransportSecret(t, err.Error(), testToken, "https://api.telegram.org")
}

func TestClientClassifiesOnlyRetryableTransportReadAndAPIFailuresAsTransient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		client    telegram.HTTPClient
		wantRetry bool
	}{
		{
			name: "network failure",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed with secret")
			}),
			wantRetry: true,
		},
		{
			name: "response read failure",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: failingReadCloser{}}, nil
			}),
			wantRetry: true,
		},
		{
			name: "explicit forbidden with unreadable body",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusForbidden, Body: failingReadCloser{}}, nil
			}),
		},
		{
			name: "rate limited",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusTooManyRequests, `{"ok":false,"error_code":429}`), nil
			}),
			wantRetry: true,
		},
		{
			name: "server unavailable",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusBadGateway, `{"ok":false,"error_code":502}`), nil
			}),
			wantRetry: true,
		},
		{
			name: "unauthorized",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusUnauthorized, `{"ok":false,"error_code":401}`), nil
			}),
		},
		{
			name: "forbidden",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusForbidden, `{"ok":false,"error_code":403}`), nil
			}),
		},
		{
			name: "poll conflict",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusConflict, `{"ok":false,"error_code":409}`), nil
			}),
		},
		{
			name: "protocol violation",
			client: httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"ok":`), nil
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := mustTestClient(t, "121212:test-only", test.client, telegram.Options{})
			_, err := client.GetUpdates(context.Background(), validGetUpdatesRequest())
			if err == nil {
				t.Fatal("GetUpdates() error = nil")
			}
			if got := telegram.IsTransient(err); got != test.wantRetry {
				t.Fatalf("IsTransient(%v) = %v, want %v", err, got, test.wantRetry)
			}
		})
	}
}

func TestClientRejectsNonSuccessStatusOrOKFalseIndependently(t *testing.T) {
	const testToken = "111111:redact-this"
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "HTTP success with API rejection",
			statusCode: http.StatusOK,
			body:       `{"ok":false,"error_code":429,"description":"secret 111111:redact-this"}`,
		},
		{
			name:       "HTTP rejection with ok true",
			statusCode: http.StatusBadGateway,
			body: `{"ok":true,"result":{"message_id":1,"from":{"id":2},` +
				`"chat":{"id":3,"type":"private"},"text":"ignored"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := mustTestClient(t, testToken, httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(test.statusCode, test.body), nil
			}), telegram.Options{})
			_, err := client.SendMessage(context.Background(), telegram.SendMessageRequest{
				ChatID: 3,
				Text:   "safe",
			})
			if err == nil {
				t.Fatal("SendMessage() error = nil, want rejection")
			}
			var apiError *telegram.APIError
			if !errors.As(err, &apiError) {
				t.Fatalf("SendMessage() error = %T, want *telegram.APIError", err)
			}
			if apiError.HTTPStatus != test.statusCode {
				t.Errorf("API HTTP status = %d, want %d", apiError.HTTPStatus, test.statusCode)
			}
			assertNoTransportSecret(t, err.Error(), testToken, "https://api.telegram.org")
		})
	}
}

func TestClientBoundsResponseLongPollAndContext(t *testing.T) {
	const testToken = "222222:test-only"
	t.Run("response body", func(t *testing.T) {
		client := mustTestClient(t, testToken, httpClientFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"ok":true,"result":[]}`+strings.Repeat(" ", 64)), nil
		}), telegram.Options{MaxResponseBytes: 24})
		_, err := client.GetUpdates(context.Background(), validGetUpdatesRequest())
		if err == nil || !strings.Contains(err.Error(), "exceeded 24 bytes") {
			t.Fatalf("oversized response error = %v", err)
		}
		assertNoTransportSecret(t, err.Error(), testToken, "https://api.telegram.org")
	})

	t.Run("client timeout", func(t *testing.T) {
		client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}), telegram.Options{RequestTimeout: 20 * time.Millisecond})
		_, err := client.GetUpdates(context.Background(), validGetUpdatesRequest())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("request timeout error = %v, want context deadline exceeded", err)
		}
		if !telegram.IsTransient(err) {
			t.Fatalf("request timeout must be retryable: %v", err)
		}
		assertNoTransportSecret(t, err.Error(), testToken, "https://api.telegram.org")
	})

	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}), telegram.Options{})
		_, err := client.GetUpdates(ctx, validGetUpdatesRequest())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation error = %v, want context canceled", err)
		}
		if telegram.IsTransient(err) {
			t.Fatalf("caller cancellation must not be retryable: %v", err)
		}
		assertNoTransportSecret(t, err.Error(), testToken, "https://api.telegram.org")
	})

	t.Run("long poll bounds", func(t *testing.T) {
		calls := 0
		client := mustTestClient(t, testToken, httpClientFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{"ok":true,"result":[]}`), nil
		}), telegram.Options{})
		invalid := []telegram.GetUpdatesRequest{
			{Limit: 0, TimeoutSeconds: 30},
			{Limit: 101, TimeoutSeconds: 30},
			{Limit: 25, TimeoutSeconds: -1},
			{Limit: 25, TimeoutSeconds: 51},
		}
		for _, request := range invalid {
			if _, err := client.GetUpdates(context.Background(), request); err == nil {
				t.Errorf("GetUpdates(%#v) error = nil, want bounds error", request)
			}
		}
		if calls != 0 {
			t.Fatalf("invalid long polls reached HTTP client %d times, want 0", calls)
		}
	})
}

func TestClientRejectsMalformedEnvelopeAndRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"ok":`},
		{name: "invalid ok type", body: `{"ok":"true","result":[]}`},
		{name: "missing ok", body: `{"result":[]}`},
		{name: "missing result", body: `{"ok":true}`},
		{name: "trailing JSON", body: `{"ok":true,"result":[]} {}`},
		{name: "missing update id", body: `{"ok":true,"result":[{"message":{"message_id":1,"from":{"id":2},"chat":{"id":3,"type":"private"}}}]}`},
		{name: "invalid update id type", body: `{"ok":true,"result":[{"update_id":"1","future":true}]}`},
		{name: "non-positive update id", body: `{"ok":true,"result":[{"update_id":0,"future":true}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := mustTestClient(t, "333333:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, test.body), nil
			}), telegram.Options{})
			if _, err := client.GetUpdates(context.Background(), validGetUpdatesRequest()); err == nil {
				t.Fatal("GetUpdates() error = nil, want malformed response rejection")
			}
		})
	}
}

func TestClientShortPollReturnsMalformedAndUnknownUpdatesAsAdvanceableSkippedItems(t *testing.T) {
	client := mustTestClient(t, "444444:test-only", httpClientFunc(func(request *http.Request) (*http.Response, error) {
		var body telegram.GetUpdatesRequest
		decodeExactRequest(t, request, &body)
		if body.TimeoutSeconds != 0 {
			t.Fatalf("short-poll timeout = %d, want 0", body.TimeoutSeconds)
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":[
  {"update_id":51,"message":"malformed"},
  {"update_id":52,"future_update":{"future":true}},
  {"update_id":53,"callback_query":{"from":{"id":2},"message":{"message_id":1,"from":{"id":4},"chat":{"id":3,"type":"private"}},"data":"missing-id"}},
  {"update_id":54,"message":{"message_id":2,"from":{"id":2},"chat":{"id":3,"type":"private"},"text":"live"}}
]}`), nil
	}), telegram.Options{})

	updates, err := client.GetUpdates(context.Background(), telegram.GetUpdatesRequest{Limit: 10, TimeoutSeconds: 0})
	if err != nil {
		t.Fatalf("GetUpdates(short poll) error = %v", err)
	}
	if len(updates) != 4 {
		t.Fatalf("updates = %#v, want four cursor items", updates)
	}
	for index, id := range []telegram.UpdateID{51, 52, 53} {
		update := updates[index]
		if update.UpdateID != id || !update.Skipped || update.Message != nil || update.CallbackQuery != nil {
			t.Errorf("updates[%d] = %#v, want skipped id %d with no payload", index, update, id)
		}
	}
	if update := updates[3]; update.UpdateID != 54 || update.Skipped || update.Message == nil {
		t.Fatalf("live update = %#v, want supported non-skipped message", update)
	}
}

func TestClientMarksMalformedOrAmbiguousTypedMediaAsSkipped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "reply id is not positive",
			payload: `"text":"hello","reply_to_message":{"message_id":0}`,
		},
		{
			name:    "voice identity missing",
			payload: `"voice":{"file_id":"","file_unique_id":"voice-u","duration":1}`,
		},
		{
			name:    "photo dimensions invalid",
			payload: `"photo":[{"file_id":"photo","file_unique_id":"photo-u","width":0,"height":10}]`,
		},
		{
			name:    "video size negative",
			payload: `"video":{"file_id":"video","file_unique_id":"video-u","width":10,"height":10,"duration":1,"file_size":-1}`,
		},
		{
			name:    "multiple media payloads",
			payload: `"voice":{"file_id":"voice","file_unique_id":"voice-u","duration":1},"photo":[{"file_id":"photo","file_unique_id":"photo-u","width":10,"height":10}]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := `{"ok":true,"result":[{"update_id":61,"message":{"message_id":2,"from":{"id":2},"chat":{"id":3,"type":"private"},` + test.payload + `}}]}`
			client := mustTestClient(t, "444445:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, body), nil
			}), telegram.Options{})
			updates, err := client.GetUpdates(context.Background(), telegram.GetUpdatesRequest{Limit: 10})
			if err != nil {
				t.Fatalf("GetUpdates() error = %v", err)
			}
			if len(updates) != 1 || !updates[0].Skipped || updates[0].Message != nil {
				t.Fatalf("updates = %#v, want skipped cursor item 61", updates)
			}
		})
	}
}

func TestBootstrapOffsetForgetsPendingUpdatesWithoutDispatchingThem(t *testing.T) {
	const testToken = "555555:test-only"
	calls := 0
	client := mustTestClient(t, testToken, httpClientFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var body telegram.GetUpdatesRequest
		decodeExactRequest(t, request, &body)
		switch calls {
		case 1:
			if body.Offset != -1 || body.Limit != 1 || body.TimeoutSeconds != 0 {
				t.Fatalf("bootstrap request = %#v, want offset=-1 limit=1 timeout=0", body)
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"result":[{
  "update_id":90,
  "message":{"message_id":1,"from":{"id":2},"chat":{"id":3,"type":"private"},"text":"stale pre-cutover command"}
}]}`), nil
		case 2:
			if body.Offset != 91 || body.Limit != 10 || body.TimeoutSeconds != 0 {
				t.Fatalf("live request = %#v, want offset=91 limit=10 timeout=0", body)
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"result":[{
  "update_id":91,
  "message":{"message_id":2,"from":{"id":2},"chat":{"id":3,"type":"private"},"text":"live post-cutover command"}
}]}`), nil
		default:
			t.Fatalf("unexpected HTTP call %d", calls)
			return nil, nil
		}
	}), telegram.Options{})

	boundary, err := client.BootstrapOffset(context.Background(), telegram.BootstrapOffsetRequest{
		ForgetPendingUpdates: true,
	})
	if err != nil {
		t.Fatalf("BootstrapOffset() error = %v", err)
	}
	if boundary != 91 {
		t.Fatalf("BootstrapOffset() = %d, want 91", boundary)
	}

	live, err := client.GetUpdates(context.Background(), telegram.GetUpdatesRequest{
		Offset: boundary, Limit: 10, TimeoutSeconds: 0,
	})
	if err != nil {
		t.Fatalf("post-cutover GetUpdates() error = %v", err)
	}
	if len(live) != 1 || live[0].UpdateID != 91 || live[0].Message == nil || live[0].Message.Text != "live post-cutover command" {
		t.Fatalf("post-cutover updates = %#v, want only update 91", live)
	}
}

func TestBootstrapOffsetRequiresExplicitForgetPendingUpdates(t *testing.T) {
	calls := 0
	client := mustTestClient(t, "565656:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusOK, `{"ok":true,"result":[]}`), nil
	}), telegram.Options{})

	if _, err := client.BootstrapOffset(context.Background(), telegram.BootstrapOffsetRequest{}); err == nil {
		t.Fatal("BootstrapOffset() error = nil, want explicit destructive intent")
	}
	if calls != 0 {
		t.Fatalf("BootstrapOffset without confirmation made %d HTTP calls, want 0", calls)
	}
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (function httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed with secret")
}

func (failingReadCloser) Close() error { return nil }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func mustTestClient(
	t *testing.T,
	token string,
	httpClient telegram.HTTPClient,
	options telegram.Options,
) *telegram.Client {
	t.Helper()
	client, err := telegram.NewClient(token, httpClient, options)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func validGetUpdatesRequest() telegram.GetUpdatesRequest {
	return telegram.GetUpdatesRequest{Limit: 25, TimeoutSeconds: 30}
}

func assertNoTransportSecret(t *testing.T, text string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("returned text exposed forbidden transport value")
		}
	}
}
