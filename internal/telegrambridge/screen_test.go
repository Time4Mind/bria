package telegrambridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/screen"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
)

type screenSourceFunc func(context.Context, string) ([]byte, error)

func (f screenSourceFunc) ScreenPNG(ctx context.Context, id string) ([]byte, error) {
	return f(ctx, id)
}

type cachedScreenSource struct {
	png          []byte
	hash         string
	fileID       string
	refreshCalls int
	remembered   int
}

func (source *cachedScreenSource) ScreenPNG(context.Context, string) ([]byte, error) {
	return append([]byte(nil), source.png...), nil
}
func (source *cachedScreenSource) CachedScreenDelivery(string) ([]byte, string, string) {
	return append([]byte(nil), source.png...), source.hash, source.fileID
}
func (source *cachedScreenSource) RequestScreenPNG(context.Context, string) error {
	source.refreshCalls++
	return nil
}
func (source *cachedScreenSource) RememberTelegramFileID(sessionID, hash, fileID string) bool {
	if sessionID != "active" || hash != source.hash || fileID == "" {
		return false
	}
	source.fileID = fileID
	source.remembered++
	return true
}

type deferredScreenSource struct {
	png     []byte
	started chan struct{}
	release chan struct{}
}

func (source *deferredScreenSource) CachedScreenPNG(string) []byte {
	return append([]byte(nil), source.png...)
}

func (source *deferredScreenSource) RequestScreenPNG(context.Context, string) error {
	select {
	case source.started <- struct{}{}:
	default:
	}
	<-source.release
	return nil
}

func TestDeferredScreenRefreshDoesNotDelayRichCardText(t *testing.T) {
	png, err := screen.RenderNative(context.Background(), "cached native screen")
	if err != nil {
		t.Fatal(err)
	}
	source := &deferredScreenSource{png: png, started: make(chan struct{}, 1), release: make(chan struct{})}
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/editMessageText") {
			t.Fatalf("unexpected transport: %s", request.URL.Path)
		}
		if err := request.ParseMultipartForm(2 << 20); err != nil {
			t.Fatal(err)
		}
		defer request.MultipartForm.RemoveAll()
		var rich telegram.InputRichMessage
		if err := json.Unmarshal([]byte(request.FormValue("rich_message")), &rich); err != nil {
			t.Fatal(err)
		}
		if len(rich.Media) != 1 {
			t.Fatal("cached screenshot was not attached")
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":91,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.BindScreenSource(source); err != nil {
		t.Fatal(err)
	}
	keyboard := coordinator.KeyboardMarkup{{{Text: "Menu", CallbackData: "signed"}}}
	started := time.Now()
	if _, err := sender.EditStatusWithKeyboard(context.Background(), "deferred-screen", coordinator.Status{ConversationID: 42, SourceMessageID: 91, ScreenSessionID: "active", Text: "text is immediate"}, &keyboard); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("rich card waited for screenshot refresh: %s", elapsed)
	}
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("screenshot refresh was not requested")
	}
	close(source.release)
}

func TestScreenPhotoEditsSameActiveCardWithKeyboardAndNoSeparateSend(t *testing.T) {
	png, err := screen.RenderNative(context.Background(), "actual native screen")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(request.URL.Path, "/editMessageText") {
			t.Fatalf("unexpected independent transport: %s", request.URL.Path)
		}
		if err := request.ParseMultipartForm(2 << 20); err != nil {
			t.Fatal(err)
		}
		defer request.MultipartForm.RemoveAll()
		if request.FormValue("message_id") != "91" || request.FormValue("chat_id") != "42" || request.FormValue("reply_markup") == "" {
			t.Fatal("card identity/keyboard lost")
		}
		var rich telegram.InputRichMessage
		if err := json.Unmarshal([]byte(request.FormValue("rich_message")), &rich); err != nil {
			t.Fatal(err)
		}
		if len(rich.Media) != 1 || rich.Media[0].ID != telegram.ScreenPhotoID || !strings.Contains(rich.Markdown, "tg://photo?id="+telegram.ScreenPhotoID) {
			t.Fatal("rich photo mapping lost")
		}
		file, _, err := request.FormFile(telegram.ScreenPhotoID)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		actual, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(actual, png) {
			t.Fatal("wrong native PNG")
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":91,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.BindScreenSource(screenSourceFunc(func(_ context.Context, id string) ([]byte, error) {
		if id != "active" {
			t.Fatal("wrong session marker")
		}
		return png, nil
	})); err != nil {
		t.Fatal(err)
	}
	keyboard := coordinator.KeyboardMarkup{{{Text: "Menu", CallbackData: "signed"}}}
	receipt, err := sender.EditStatusWithKeyboard(context.Background(), "screen-edit", coordinator.Status{ConversationID: 42, SourceMessageID: 91, ScreenSessionID: "active", Text: "session context"}, &keyboard)
	if err != nil || receipt.MessageID != 91 || calls != 1 {
		t.Fatalf("edit=%#v calls=%d err=%v", receipt, calls, err)
	}
}

func TestConfirmedRichPhotoIsReusedByFileIDWithoutSecondUpload(t *testing.T) {
	png, err := screen.RenderNative(context.Background(), "actual native screen")
	if err != nil {
		t.Fatal(err)
	}
	source := &cachedScreenSource{png: png, hash: "final-png-hash"}
	calls := 0
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if err := request.ParseMultipartForm(2 << 20); err != nil {
				t.Fatal(err)
			}
			defer request.MultipartForm.RemoveAll()
			if _, _, err := request.FormFile(telegram.ScreenPhotoID); err != nil {
				t.Fatalf("first rich update did not upload PNG: %v", err)
			}
		case 2:
			if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("second rich update content type = %q, want JSON file_id reuse", got)
			}
			var body telegram.EditMessageTextRequest
			decodeJSON(t, request, &body)
			if body.RichMessage == nil || len(body.RichMessage.Media) != 1 || body.RichMessage.Media[0].Media.Media != "confirmed-rich-photo" {
				t.Fatalf("second rich update = %#v", body)
			}
		default:
			t.Fatalf("unexpected transport call %d", calls)
		}
		return response(http.StatusOK, `{
			"ok":true,
			"result":{
				"message_id":91,
				"from":{"id":600,"is_bot":true},
				"chat":{"id":42,"type":"private"},
				"rich_message":{"media":[{"file_id":"confirmed-rich-photo","width":800,"height":400,"file_size":1000}]}
			}
		}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.BindScreenSource(source); err != nil {
		t.Fatal(err)
	}
	status := coordinator.Status{ConversationID: 42, SourceMessageID: 91, ScreenSessionID: "active", Text: "context"}
	if _, err := sender.EditStatusWithKeyboard(context.Background(), "first", status, nil); err != nil {
		t.Fatal(err)
	}
	if source.fileID != "confirmed-rich-photo" || source.remembered != 1 {
		t.Fatalf("receipt was not cached: file_id=%q remembered=%d", source.fileID, source.remembered)
	}
	if _, err := sender.EditStatusWithKeyboard(context.Background(), "second", status, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || source.refreshCalls != 2 || source.remembered != 1 {
		t.Fatalf("calls=%d refresh=%d remembered=%d", calls, source.refreshCalls, source.remembered)
	}
}

func TestScreenOffRemovesMediaInSameRichCard(t *testing.T) {
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/editMessageText") {
			t.Fatal(request.URL.Path)
		}
		var body telegram.EditMessageTextRequest
		decodeJSON(t, request, &body)
		if body.MessageID != 91 || body.RichMessage == nil || len(body.RichMessage.Media) != 0 || strings.Contains(body.RichMessage.Markdown, "tg://photo") {
			t.Fatal("disabled Screen did not remove media from same card")
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":91,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.BindScreenSource(screenSourceFunc(func(context.Context, string) ([]byte, error) { return nil, nil })); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.EditStatusWithKeyboard(context.Background(), "off", coordinator.Status{ConversationID: 42, SourceMessageID: 91, ScreenSessionID: "active", Text: "context"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnboundScreenMarkerPreservesPlainTransport(t *testing.T) {
	for _, mode := range []string{"send", "keyboard", "edit"} {
		t.Run(mode, func(t *testing.T) {
			client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
				want := "/sendMessage"
				if mode == "edit" {
					want = "/editMessageText"
				}
				if !strings.HasSuffix(request.URL.Path, want) {
					t.Fatalf("unbound screen changed transport: %s", request.URL.Path)
				}
				var body map[string]json.RawMessage
				decodeJSON(t, request, &body)
				if _, ok := body["rich_message"]; ok {
					t.Fatal("unbound screen marker produced rich message")
				}
				if len(body["text"]) == 0 {
					t.Fatal("plain message text missing")
				}
				return response(http.StatusOK, `{"ok":true,"result":{"message_id":91,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
			})
			sender, err := telegrambridge.NewSender(client)
			if err != nil {
				t.Fatal(err)
			}
			status := coordinator.Status{ConversationID: 42, SourceMessageID: 91, ScreenSessionID: "active", Text: "context"}
			keyboard := coordinator.KeyboardMarkup{{{Text: "Menu", CallbackData: "signed"}}}
			switch mode {
			case "send":
				_, err = sender.SendStatus(context.Background(), "send", status)
			case "keyboard":
				_, err = sender.SendStatusWithKeyboard(context.Background(), "keys", status, &keyboard)
			case "edit":
				_, err = sender.EditStatusWithKeyboard(context.Background(), "edit", status, &keyboard)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
