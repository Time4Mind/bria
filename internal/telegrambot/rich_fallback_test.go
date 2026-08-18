package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestRichFallbackKeepsTechnicalBlocksExpandable(t *testing.T) {
	got := richFallbackMarkdownV2(telegramui.Screen{Text: strings.Join([]string{
		"Session header",
		"<details><summary>▷ Bash · ⏳ 0:04</summary>\n\n```bash\nnmap -sn 192.168.0.0/24\n```\n\n</details>",
		"agent commentary",
	}, "\n\n")})
	for _, want := range []string{
		"Session header",
		">▷ Bash · ⏳ 0:04\n>\\`\\`\\`bash\n>nmap \\-sn 192\\.168\\.0\\.0/24\n>\\`\\`\\`||",
		"agent commentary",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fallback missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "<details>") {
		t.Fatalf("Rich details leaked into MarkdownV2 fallback: %q", got)
	}
}

func TestOversizedRichFallbackKeepsSummariesAndDropsTechnicalBodies(t *testing.T) {
	got := richFallbackMarkdownV2(telegramui.Screen{Text: "<details><summary>✓ Bash</summary>\n\n" +
		strings.Repeat("_*[]()~`>#+-=|{}.!", 400) + "\n\n</details>",
	})
	if len(got) > MaxMessageTextBytes {
		t.Fatalf("fallback has %d bytes", len(got))
	}
	if !strings.Contains(got, "✓ Bash") || strings.Contains(got, "\\_\\*") {
		t.Fatalf("oversized fallback did not retain only the summary: %q", got)
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
		if payload.ParseMode != string(telegramui.ParseModeMarkdownV2) ||
			strings.Contains(payload.Text, "<details>") ||
			!strings.Contains(payload.Text, ">✓ Bash\n>echo ok||") {
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
