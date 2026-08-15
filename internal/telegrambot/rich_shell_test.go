package telegrambot

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestRichTextNormalizesShellFenceLikeCCBotOnAndroid(t *testing.T) {
	rich, err := buildRichTextMessage(
		"<details><summary>✓ Bash</summary>\n\n```bash\nprintf one\nprintf 'x < y && z > y'\n```\n\n</details>",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "<details><summary>✓ Bash</summary>\n\n" +
		"<code>printf one\nprintf 'x &lt; y &amp;&amp; z &gt; y'</code>\n\n</details>"
	if rich.Markdown != want {
		t.Fatalf("rich shell block=\n%s\nwant=\n%s", rich.Markdown, want)
	}
	if strings.Contains(rich.Markdown, "```") {
		t.Fatalf("literal fence remains in rich shell block: %q", rich.Markdown)
	}
}

func TestRichTextNormalizesSingleLineShellFenceToInlineCode(t *testing.T) {
	rich, err := buildRichTextMessage("```sh\necho ok\n```")
	if err != nil {
		t.Fatal(err)
	}
	if rich.Markdown != "`echo ok`" {
		t.Fatalf("single-line shell block = %q", rich.Markdown)
	}
}

func TestRichPaneAlsoNormalizesShellFence(t *testing.T) {
	text := "head\n```bash\none\ntwo\n```\ntail"
	anchor := len("head\n")
	rich, err := buildRichMessage(text, telegramui.PaneImage{AnchorOffset: anchor}, "photo-id")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rich.Markdown, "```") ||
		!strings.Contains(rich.Markdown, "<code>one\ntwo</code>") ||
		!strings.Contains(rich.Markdown, richPhotoMarkdown) {
		t.Fatalf("rich pane shell block was not normalized: %q", rich.Markdown)
	}
}
