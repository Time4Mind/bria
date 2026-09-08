package cardtranscript_test

import (
	"encoding/json"
	"html"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/cardtranscript"
	"bria/internal/tooltext"
)

func renderedTool(t *testing.T, tool cardtranscript.Tool) string {
	t.Helper()
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	return cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: string(raw)}})[0]
}

func TestToolAllBudgetsAfterStoredNativeContent(t *testing.T) {
	for _, choice := range []int{0, 5, 10, 20, 40, -1, 7} {
		budget := choice
		if budget != 5 && budget != 20 && budget != 40 {
			budget = 10
		}
		for _, overflow := range []bool{false, true} {
			text := strings.Repeat("界", budget*100)
			if overflow {
				text += "X"
			}
			raw, _ := json.Marshal([]map[string]string{{"type": "text", "text": text}})
			retained := tooltext.Retain(string(raw))
			encoded := cardtranscript.EncodeTool(cardtranscript.Tool{Name: "exec", Output: retained})
			got := spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: encoded, ToolLines: choice}})[0])
			want := strings.TrimSuffix(strings.Repeat(strings.Repeat("界", 100)+"\n", budget), "\n")
			if overflow {
				want += "\n… (truncated)"
			}
			if got != want {
				t.Fatalf("choice=%d overflow=%t got %d runes", choice, overflow, utf8.RuneCountInString(got))
			}
		}
	}
}

func TestStoredToolUsesOneArgumentsAndOutputBudget(t *testing.T) {
	encoded := cardtranscript.EncodeTool(cardtranscript.Tool{Name: "exec", Arguments: strings.Repeat("a", 100), Output: strings.Repeat("b", 4000)})
	got := spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: encoded, ToolLines: 5}})[0])
	want := strings.Repeat("a", 100) + "\n\n---\n\n" + strings.Repeat("b", 100) + "\n… (truncated)"
	if got != want {
		t.Fatalf("shared budget: %q", got)
	}
	encoded = cardtranscript.EncodeTool(cardtranscript.Tool{Name: "exec", Arguments: strings.Repeat("a", 4000), Output: "hidden output"})
	got = spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: encoded, ToolLines: 40}})[0])
	want = strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 100)+"\n", 40), "\n") + "\n… (truncated)"
	if got != want {
		t.Fatal("output stole arguments budget or truncation notice missing")
	}
}

func TestStoredNativeBlocksPreserveEscapesAndEmptyArguments(t *testing.T) {
	text := "  `\\n` C:\\new\\tools\\file \\d+ & <x>\n| a | b |\n| --- | --- |\ntrailing  "
	raw, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": text}})
	encoded := cardtranscript.EncodeTool(cardtranscript.Tool{Name: "exec", Output: tooltext.Retain(string(raw))})
	got := spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: encoded}})[0])
	if got != text {
		t.Fatalf("literal content altered: %q", got)
	}
}

func TestOldCallAndNewResultKeepExactLongIdentity(t *testing.T) {
	id := strings.Repeat("\x01", 300)
	call, _ := json.Marshal(cardtranscript.Tool{ID: id, Name: "exec", Arguments: "pwd", Status: "running"})
	result := cardtranscript.EncodeTool(cardtranscript.Tool{ID: id, Output: "done", Status: "completed"})
	blocks := cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: string(call)}, {Kind: "tool", Text: result}})
	if len(blocks) != 1 || spoilerBody(t, blocks[0]) != "pwd\n\n---\n\ndone" {
		t.Fatalf("old call and new result separated: %d blocks", len(blocks))
	}
}

func TestStoredToolFortyUnicodeLinesSurvivePagination(t *testing.T) {
	for _, glyph := range []string{"🙂", "界", "<", "\x01"} {
		t.Run(glyph, func(t *testing.T) {
			encoded := cardtranscript.EncodeTool(cardtranscript.Tool{ID: strings.Repeat("i", 1024), Name: "exec", Output: strings.Repeat(glyph, 4000)})
			if len(encoded) > 16384 || !json.Valid([]byte(encoded)) {
				t.Fatal("invalid persisted envelope")
			}
			blocks := cardtranscript.RenderBlocks([]cardtranscript.Block{{Kind: "tool", Text: encoded, ToolLines: 40}})
			pages := cardtranscript.Paginate(blocks, 32)
			var restored strings.Builder
			for _, page := range pages {
				if len(page.Content) > 3000 || !utf8.ValidString(page.Content) {
					t.Fatal("invalid page")
				}
				restored.WriteString(spoilerBody(t, page.Content))
			}
			want := strings.TrimSuffix(strings.Repeat(strings.Repeat(glyph, 100)+"\n", 40), "\n")
			if restored.String() != want {
				t.Fatalf("lost stored content: got %d runes, want %d", utf8.RuneCountInString(restored.String()), utf8.RuneCountInString(want))
			}
		})
	}
}

func spoilerBody(t *testing.T, text string) string {
	t.Helper()
	_, body, ok := strings.Cut(text, "</summary>\n\n")
	if !ok || !strings.HasSuffix(body, "\n\n</details>") {
		t.Fatalf("invalid spoiler: %q", text)
	}
	return html.UnescapeString(strings.TrimSuffix(body, "\n\n</details>"))
}

func TestToolDefaultBudgetCountsWrappedUnicodeLines(t *testing.T) {
	body := spoilerBody(t, renderedTool(t, cardtranscript.Tool{Name: "exec", Output: strings.Repeat("🙂", 1001)}))
	want := strings.TrimSuffix(strings.Repeat(strings.Repeat("🙂", 100)+"\n", 10), "\n") + "\n… (truncated)"
	if body != want {
		t.Fatalf("default must keep ten full 100-rune lines plus notice: got %d runes", len([]rune(body)))
	}
}

func TestToolNativeTextBlocksDecodeWithoutChangingLiteralSyntax(t *testing.T) {
	text := "<details> & | a | b |\n```sh\nprintf '%s\\n' \\\"x\\\"\nC:\\new\\tools\\file \\d+\n```"
	blocks, _ := json.Marshal([]map[string]string{
		{"type": "text", "text": text},
		{"type": "image", "data": "not text"},
		{"type": "input_text", "text": "second"},
	})
	rendered := renderedTool(t, cardtranscript.Tool{Name: "exec", Output: string(blocks)})
	if got := spoilerBody(t, rendered); got != text+"\nsecond" {
		t.Fatalf("native blocks changed literal text: %q", got)
	}
	if strings.Contains(rendered, "<details> &") || !strings.Contains(rendered, "&lt;details&gt; &amp;") {
		t.Fatalf("unsafe escaping: %q", rendered)
	}
}
