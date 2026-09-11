package telegrambridge_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
)

type spacingHTTPClient func(*http.Request) (*http.Response, error)

func (client spacingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client(request)
}

func TestCardDividerHardBreakOnUniformRichWire(t *testing.T) {
	// The semantic card header owns the explicit Rich Markdown hard break.
	const header = "📋 Session\n\n─────  \n"
	for _, body := range []struct {
		name, markdown, rich string
	}{
		{"ordinary", "**Answer**\n\nNext", "**Answer**\n\nNext"},
		{"code", "```go\nanswer()\n```", `<pre><code class="language-go">answer()</code></pre>`},
		{"list", "- **First**\n- Second", "- **First**\n- Second"},
	} {
		for _, mode := range []string{"legacy-off", "legacy-on"} {
			for _, action := range []string{"send", "send-keyboard", "edit"} {
				t.Run(body.name+"/"+mode+"/"+action, func(t *testing.T) {
					var wire struct {
						Text     string                     `json:"text"`
						Entities []telegram.MessageEntity   `json:"entities"`
						Rich     *telegram.InputRichMessage `json:"rich_message"`
					}
					var method string
					client, err := telegram.NewClient("123:spacing-test", spacingHTTPClient(func(request *http.Request) (*http.Response, error) {
						method = request.URL.Path
						if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
							t.Fatal(err)
						}
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
					}), telegram.Options{})
					if err != nil {
						t.Fatal(err)
					}
					sender, err := telegrambridge.NewSender(client)
					if err != nil {
						t.Fatal(err)
					}
					status := coordinator.Status{ConversationID: 42, SourceMessageID: 55, Text: header + body.markdown, RichMarkdown: mode == "legacy-on"}
					keyboard := coordinator.KeyboardMarkup{{{Text: "Menu", CallbackData: "signed"}}}
					switch action {
					case "send":
						_, err = sender.SendStatus(context.Background(), "spacing", status)
					case "send-keyboard":
						_, err = sender.SendStatusWithKeyboard(context.Background(), "spacing", status, &keyboard)
					case "edit":
						_, err = sender.EditStatusWithKeyboard(context.Background(), "spacing", status, &keyboard)
					}
					if err != nil {
						t.Fatal(err)
					}
					wantMethod := "/sendRichMessage"
					if action == "edit" {
						wantMethod = "/editMessageText"
					}
					if !strings.HasSuffix(method, wantMethod) {
						t.Fatalf("wire method = %q, want %q", method, wantMethod)
					}
					if wire.Rich == nil || wire.Rich.Markdown != header+body.rich || wire.Text != "" || len(wire.Entities) != 0 {
						t.Fatalf("wire = %+v, want uniform Rich hardbreak regardless of legacy flag", wire)
					}
				})
			}
		}
	}
}

func TestActiveCardExecUsesNativeRichCodeOnWire(t *testing.T) {
	var wire struct {
		Rich *telegram.InputRichMessage `json:"rich_message"`
	}
	client, err := telegram.NewClient("123:rich-code-test", spacingHTTPClient(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close(context.Background())
	body := "<details><summary>✓ exec</summary>\n\n```shell\nprintf '<tag>&value\\n'\n```\n\n---\n\nplain &lt;result&gt;&amp;\n\n</details>"
	status := coordinator.Status{ConversationID: 42, SourceMessageID: 55, Text: "workdir · local · codex · работает\n\n─────  \n" + body, RichMarkdown: true}
	keyboard := coordinator.KeyboardMarkup{{{Text: "1/1", CallbackData: "signed"}}}
	if _, err := sender.EditStatusWithKeyboard(context.Background(), "active-card-code", status, &keyboard); err != nil {
		t.Fatal(err)
	}
	if wire.Rich == nil || strings.Contains(wire.Rich.Markdown, "```") ||
		!strings.Contains(wire.Rich.Markdown, `<details><summary>✓ exec</summary>`) ||
		!strings.Contains(wire.Rich.Markdown, `<pre><code class="language-shell">printf &#39;&lt;tag&gt;&amp;value\n&#39;</code></pre>`) ||
		!strings.Contains(wire.Rich.Markdown, "\n\n---\n\nplain &lt;result&gt;&amp;") {
		t.Fatalf("active card tool formatting = %q", wire.Rich.Markdown)
	}
}

func TestActiveCardMarkdownQuoteUsesNativeRichBlockquoteOnWire(t *testing.T) {
	var wire struct {
		Rich *telegram.InputRichMessage `json:"rich_message"`
	}
	client, err := telegram.NewClient("123:rich-quote-test", spacingHTTPClient(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close(context.Background())
	body := "Exact preview:\n\n&gt; Проверил прототип.\n&gt;\n&gt; Что проверено:\n&gt; - нет дублей;\n&gt; - арифметика сходится.\n\nСледующий абзац."
	status := coordinator.Status{ConversationID: 42, SourceMessageID: 55, Text: "workdir · local · codex\n\n─────  \n" + body, RichMarkdown: true}
	keyboard := coordinator.KeyboardMarkup{{{Text: "1/1", CallbackData: "signed"}}}
	if _, err := sender.EditStatusWithKeyboard(context.Background(), "active-card-quote", status, &keyboard); err != nil {
		t.Fatal(err)
	}
	if wire.Rich == nil || !strings.Contains(wire.Rich.Markdown, "<blockquote>Проверил прототип.\n\nЧто проверено:\n- нет дублей;\n- арифметика сходится.</blockquote>") ||
		strings.Contains(wire.Rich.Markdown, "&gt;") || !strings.HasSuffix(wire.Rich.Markdown, "</blockquote>\n\nСледующий абзац.") {
		t.Fatalf("active card quote formatting = %q", wire.Rich.Markdown)
	}
}
