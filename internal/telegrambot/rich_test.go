package telegrambot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/terminalimage"
)

func TestRichScreenUploadsAtAnchorAndReusesLargestPhoto(t *testing.T) {
	pane := testPanePNG(t)
	var mu sync.Mutex
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		mu.Lock()
		methods = append(methods, method)
		callNumber := len(methods)
		mu.Unlock()
		switch callNumber {
		case 1:
			if method != "sendRichMessage" || !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
				t.Errorf("first request = %s %s", method, request.Header.Get("Content-Type"))
			}
			if err := request.ParseMultipartForm(MaxRichPanePNGBytes + 1024); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			assertRichMarkdownGolden(t, request.FormValue("rich_message"))
			file, _, err := request.FormFile(richPhotoID)
			if err != nil {
				t.Fatalf("pane upload: %v", err)
			}
			uploaded, _ := io.ReadAll(file)
			_ = file.Close()
			if string(uploaded) != string(pane) {
				t.Fatal("uploaded pane differs")
			}
			writeJSON(t, writer, richMessageResponse())
		case 2:
			if method != "editMessageText" || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("second request = %s %s", method, request.Header.Get("Content-Type"))
			}
			var payload editRichMessagePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode edit payload: %v", err)
			}
			if got := payload.RichMessage.Media[0].Media.Media; got != "large-photo" {
				t.Errorf("reused photo = %q", got)
			}
			writeJSON(t, writer, map[string]any{"ok": true, "result": true})
		}
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	screen := richTestScreen(pane)
	message, err := client.SendScreen(context.Background(), 42, screen)
	if err != nil {
		t.Fatalf("SendScreen: %v", err)
	}
	if !message.Rich || message.RichMediaFileID != "large-photo" || message.PaneHash != "pane-v1" {
		t.Fatalf("rich message = %#v", message)
	}
	if _, err := client.EditScreen(context.Background(), message, screen); err != nil {
		t.Fatalf("EditScreen: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(methods, ","); got != "sendRichMessage,editMessageText" {
		t.Fatalf("methods = %q", got)
	}
}

func TestRichFailureFallsBackToHTMLText(t *testing.T) {
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		methods = append(methods, method)
		if method == "sendRichMessage" {
			writer.WriteHeader(http.StatusBadRequest)
			writeJSON(t, writer, map[string]any{"ok": false, "error_code": 400, "description": "unsupported"})
			return
		}
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 7, "chat": map[string]any{"id": 42}},
		})
	}))
	defer server.Close()
	message, err := newFakeServerClient(t, server, time.Second).SendScreen(
		context.Background(), 42, richTestScreen(testPanePNG(t)),
	)
	if err != nil {
		t.Fatalf("SendScreen: %v", err)
	}
	if message.Rich {
		t.Fatalf("fallback message marked rich: %#v", message)
	}
	if got := strings.Join(methods, ","); got != "sendRichMessage,sendMessage" {
		t.Fatalf("methods = %q", got)
	}
}

func TestRichTextScreenNeedsNoMediaAndFallsBackWithoutDetailsMarkup(t *testing.T) {
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		methods = append(methods, method)
		if method == "sendRichMessage" {
			var payload sendRichMessagePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.RichMessage.Media) != 0 ||
				!strings.Contains(payload.RichMessage.Markdown, "<details><summary>✓ Bash") {
				t.Fatalf("rich payload=%#v", payload.RichMessage)
			}
			writer.WriteHeader(http.StatusBadRequest)
			writeJSON(t, writer, map[string]any{"ok": false, "error_code": 400})
			return
		}
		var payload sendMessagePayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(payload.Text, "<details>") ||
			!strings.Contains(payload.Text, "✓ Bash\necho ok") {
			t.Fatalf("fallback text=%q", payload.Text)
		}
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 8, "chat": map[string]any{"id": 42}},
		})
	}))
	defer server.Close()
	screen := telegramui.Screen{
		Name:         telegramui.ScreenSessionCard,
		Text:         "<details><summary>✓ Bash</summary>\n\necho ok\n\n</details>",
		RichMarkdown: true,
		Grid: telegramui.Grid{telegramui.Row{{
			Label: "Menu", Callback: telegramui.Callback{Action: telegramui.ActionMenu},
		}}},
	}
	message, err := newFakeServerClient(t, server, time.Second).SendScreen(
		context.Background(), 42, screen,
	)
	if err != nil {
		t.Fatal(err)
	}
	if message.Rich || strings.Join(methods, ",") != "sendRichMessage,sendMessage" {
		t.Fatalf("message=%#v methods=%v", message, methods)
	}
}

func TestRichTextNormalizesNativeMarkdownTableLikeCCBot(t *testing.T) {
	rich, err := buildRichTextMessage("Status\n| Server | Used |\n|---|---:|\n| node | 12% |")
	if err != nil {
		t.Fatal(err)
	}
	want := "Status\n\n| <sub>Server</sub> | <sub>Used</sub> |\n|---|---:|\n| <sub>node</sub> | <sub>12%</sub> |"
	if rich.Markdown != want {
		t.Fatalf("rich table=\n%s\nwant=\n%s", rich.Markdown, want)
	}
}

func TestRichTextEscapesUnknownAgentTagsWithoutBreakingDetails(t *testing.T) {
	rich, err := buildRichTextMessage(
		"<details><summary>✓ Read</summary>\n\n" +
			"<current_date>2026-08-15</current_date> x < y\n" +
			"`<literal>`\n```xml\n<inside-code>\n```\n\n</details>",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "<details><summary>✓ Read</summary>\n\n" +
		"&lt;current_date>2026-08-15&lt;/current_date> x &lt; y\n" +
		"`<literal>`\n```xml\n<inside-code>\n```\n\n</details>"
	if rich.Markdown != want {
		t.Fatalf("rich unknown-tag escaping=\n%s\nwant=\n%s", rich.Markdown, want)
	}
}

func TestRichUploadEditClearsStaleFileIDWhenTelegramReturnsTrue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("content type = %q", request.Header.Get("Content-Type"))
		}
		writeJSON(t, writer, map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	previous := Message{
		ChatID: 42, MessageID: 9, Rich: true,
		RichMediaFileID: "old-photo", PaneHash: "old-pane",
	}
	screen := richTestScreen(testPanePNG(t))
	screen.Pane.Hash = "new-pane"
	updated, err := client.EditScreen(context.Background(), previous, screen)
	if err != nil {
		t.Fatalf("EditScreen: %v", err)
	}
	if updated.RichMediaFileID != "" || updated.PaneHash != "new-pane" {
		t.Fatalf("updated message = %#v", updated)
	}
}

func TestLegacyCardWithRichBodyKeepsLegacyCarrier(t *testing.T) {
	methods := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:])
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 9, "chat": map[string]any{"id": 42}},
		})
	}))
	defer server.Close()
	screen := telegramui.Screen{
		Name:         telegramui.ScreenSessionCard,
		Text:         "head\n\n<details><summary>tool</summary>\n\nbody\n\n</details>",
		RichMarkdown: true,
		Grid: telegramui.Grid{telegramui.Row{{
			Label: "Menu", Callback: telegramui.Callback{Action: telegramui.ActionMenu},
		}}},
	}
	updated, err := newFakeServerClient(t, server, time.Second).EditScreen(
		context.Background(), Message{ChatID: 42, MessageID: 9}, screen,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Rich || strings.Join(methods, ",") != "editMessageText" {
		t.Fatalf("message=%#v methods=%v", updated, methods)
	}
}

func TestRichCarrierStaysRichForPlainConfirmation(t *testing.T) {
	methods := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:])
		var payload editRichMessagePayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.RichMessage.Markdown != "Close this session?" {
			t.Fatalf("rich confirmation=%#v", payload.RichMessage)
		}
		writeJSON(t, writer, map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()
	updated, err := newFakeServerClient(t, server, time.Second).EditScreen(
		context.Background(), Message{ChatID: 42, MessageID: 9, Rich: true},
		telegramui.Screen{
			Name: telegramui.ScreenSessionCard, Text: "Close this session?",
			Grid: telegramui.Grid{telegramui.Row{{
				Label: "Cancel", Callback: telegramui.Callback{Action: telegramui.ActionMenu},
			}}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Rich || strings.Join(methods, ",") != "editMessageText" {
		t.Fatalf("message=%#v methods=%v", updated, methods)
	}
}

func TestRichEditTreatsUnchangedMessageAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		writeJSON(t, writer, map[string]any{
			"ok": false, "error_code": 400,
			"description": "Bad Request: message is not modified: specified new message content and reply markup are exactly the same",
		})
	}))
	defer server.Close()
	previous := Message{ChatID: 42, MessageID: 9, Rich: true, PaneHash: "same"}
	updated, err := newFakeServerClient(t, server, time.Second).EditScreen(
		context.Background(), previous,
		telegramui.Screen{Name: telegramui.ScreenSessionCard, Text: "same", RichMarkdown: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated != previous {
		t.Fatalf("unchanged rich carrier=%#v want=%#v", updated, previous)
	}
}

func TestOversizedRichPNGSkipsRichHTTPAndUsesFallback(t *testing.T) {
	methods := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:])
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 7, "chat": map[string]any{"id": 42}},
		})
	}))
	defer server.Close()
	pngBytes := make([]byte, MaxRichPanePNGBytes+1)
	copy(pngBytes, "\x89PNG\r\n\x1a\n")
	screen := richTestScreen(pngBytes)
	if _, err := newFakeServerClient(t, server, time.Second).SendScreen(context.Background(), 42, screen); err != nil {
		t.Fatalf("SendScreen: %v", err)
	}
	if got := strings.Join(methods, ","); got != "sendMessage" {
		t.Fatalf("methods = %q", got)
	}
}

func TestRichFailureTimeoutIsBoundedBeforeFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		if method == "sendRichMessage" {
			time.Sleep(100 * time.Millisecond)
			return
		}
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 7, "chat": map[string]any{"id": 42}},
		})
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{
		Token: testToken, BaseURL: server.URL, HTTPClient: server.Client(),
		RequestTimeout: time.Second, RichRequestTimeout: 20 * time.Millisecond,
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	started := time.Now()
	if _, err := client.SendScreen(context.Background(), 42, richTestScreen(testPanePNG(t))); err != nil {
		t.Fatalf("SendScreen: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("rich fallback took %s", elapsed)
	}
}

func richTestScreen(pngBytes []byte) telegramui.Screen {
	text := "<b>head</b>\ntail"
	return telegramui.Screen{
		Name: telegramui.ScreenSessionCard, Text: text, ParseMode: telegramui.ParseModeHTML,
		Grid: telegramui.Grid{telegramui.Row{{
			Label: "Menu", Callback: telegramui.Callback{Action: telegramui.ActionMenu},
		}}},
		Pane: &telegramui.PaneImage{PNG: pngBytes, Hash: "pane-v1", AnchorOffset: len("<b>head</b>\n")},
	}
}

func testPanePNG(t *testing.T) []byte {
	t.Helper()
	result, err := terminalimage.Render("\x1b[32mready\x1b[0m", terminalimage.Options{FontSize: 12})
	if err != nil {
		t.Fatalf("render pane: %v", err)
	}
	return result.PNG
}

func assertRichMarkdownGolden(t *testing.T, encoded string) {
	t.Helper()
	var payload richMessage
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		t.Fatalf("decode rich_message: %v", err)
	}
	want := "<b>head</b>\n\n\u00a0\n\n![](tg://photo?id=terminal_screenshot)\n\n\u00a0\n\ntail"
	if payload.Markdown != want {
		t.Fatalf("rich markdown mismatch\n--- got ---\n%s\n--- want ---\n%s", payload.Markdown, want)
	}
	if len(payload.Media) != 1 || payload.Media[0].ID != richPhotoID ||
		payload.Media[0].Media.Media != "attach://"+richPhotoID {
		t.Fatalf("rich media = %#v", payload.Media)
	}
}

func richMessageResponse() map[string]any {
	return map[string]any{
		"ok": true,
		"result": map[string]any{
			"message_id": 9, "chat": map[string]any{"id": 42},
			"rich_message": map[string]any{"blocks": []any{map[string]any{
				"type": "photo", "photo": []any{
					map[string]any{"file_id": "small-photo", "width": 80, "height": 40, "file_size": 100},
					map[string]any{"file_id": "large-photo", "width": 800, "height": 400, "file_size": 1000},
				},
			}}},
		},
	}
}
