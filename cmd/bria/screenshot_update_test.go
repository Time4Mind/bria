package main

import (
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/screen"
	"bria/internal/screenproduction"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type screenshotHTTP func(*http.Request) (*http.Response, error)

func (f screenshotHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }
func mustScreenshotClient(t *testing.T, f func(*http.Request) (*http.Response, error)) *telegram.Client {
	t.Helper()
	c, err := telegram.NewClient("123:synthetic-screenshot-token", screenshotHTTP(f), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func screenshotResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func decodeScreenshotJSON(t *testing.T, r *http.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

type currentNativeFixture struct {
	mu         sync.Mutex
	active     domain.SessionID
	text, hash string
}

func (f *currentNativeFixture) LoadActiveSession(context.Context) (domain.SessionID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, nil
}
func (f *currentNativeFixture) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sessionruntime.NativeSnapshot{FullText: f.text, Hash: f.hash}, true
}
func (*currentNativeFixture) NativeScreenUpdates() <-chan domain.SessionID { return nil }

func TestCurrentNativeScreenshotAppearsInSameRichUpdateFromFirstFrame(t *testing.T) {
	ctx := context.Background()
	preferences := settings.NewMemoryStore()
	if err := preferences.Update(ctx, func(s *settings.Settings) error { s.ScreenEnabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	native := &currentNativeFixture{active: "active"}
	source, err := screenproduction.NewNativeSource(preferences, native, native)
	if err != nil {
		t.Fatal(err)
	}
	var expected []byte
	reuse := false
	httpCalls := 0
	frame := 0
	client := mustScreenshotClient(t, func(request *http.Request) (*http.Response, error) {
		httpCalls++
		if !strings.HasSuffix(request.URL.Path, "/editMessageText") {
			t.Fatal("independent screenshot post")
		}
		if reuse {
			var body telegram.EditMessageTextRequest
			decodeScreenshotJSON(t, request, &body)
			if body.RichMessage == nil || len(body.RichMessage.Media) != 1 || body.RichMessage.Media[0].Media.Media != "current-photo" {
				t.Fatal("exact current PNG receipt not reused")
			}
		} else if len(expected) == 0 {
			var body telegram.EditMessageTextRequest
			decodeScreenshotJSON(t, request, &body)
			if body.RichMessage == nil || len(body.RichMessage.Media) != 0 {
				t.Fatal("disabled/stale screenshot retained")
			}
		} else {
			if err := request.ParseMultipartForm(2 << 20); err != nil {
				t.Fatal(err)
			}
			if request.MultipartForm == nil {
				t.Fatal("same-update first/current frame missing")
			}
			defer request.MultipartForm.RemoveAll()
			file, _, err := request.FormFile(telegram.ScreenPhotoID)
			if err != nil {
				t.Fatal("same-update screenshot missing", err)
			}
			defer file.Close()
			actual, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(actual, expected) {
				t.Fatal("screenshot belongs to previous update")
			}
			if request.FormValue("message_id") != "91" {
				t.Fatal("carrier changed")
			}
		}
		return screenshotResponse(http.StatusOK, `{"ok":true,"result":{"message_id":91,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"rich_message":{"media":[{"file_id":"current-photo","width":800,"height":400,"file_size":1000}]}}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.BindScreenSource(source); err != nil {
		t.Fatal(err)
	}
	status := coordinator.Status{ConversationID: 42, SourceMessageID: 91, ScreenSessionID: "active", Text: "same rich card"}
	for range 3 {
		frame++
		text := fmt.Sprintf("fresh frame %d", frame)
		native.mu.Lock()
		native.text, native.hash = text, text
		native.mu.Unlock()
		expected, err = screen.RenderNative(ctx, text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sender.EditStatusWithKeyboard(ctx, fmt.Sprint(frame), status, nil); err != nil {
			t.Fatal(err)
		}
	}
	reuse = true
	if _, err := sender.EditStatusWithKeyboard(ctx, "reuse", status, nil); err != nil {
		t.Fatal(err)
	}
	reuse = false
	expected = nil
	native.mu.Lock()
	native.active = "other"
	native.mu.Unlock()
	if _, err := sender.EditStatusWithKeyboard(ctx, "stale", status, nil); err != nil {
		t.Fatal(err)
	}
	native.mu.Lock()
	native.active = "active"
	native.mu.Unlock()
	if err := preferences.Update(ctx, func(s *settings.Settings) error { s.ScreenEnabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.EditStatusWithKeyboard(ctx, "disabled", status, nil); err != nil {
		t.Fatal(err)
	}
	if err := preferences.Update(ctx, func(s *settings.Settings) error { s.ScreenEnabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	native.mu.Lock()
	native.text, native.hash = string([]byte{0xff}), "invalid-current"
	native.mu.Unlock()
	if _, err := sender.EditStatusWithKeyboard(ctx, "bad-frame-text-only", status, nil); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	before := httpCalls
	if _, err := sender.EditStatusWithKeyboard(cancelled, "cancelled", status, nil); err == nil || httpCalls != before {
		t.Fatal("cancelled update reached HTTP")
	}
}
