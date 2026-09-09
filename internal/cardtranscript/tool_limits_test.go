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

func TestToolCommandAndOutputEachKeepDefaultBudget(t *testing.T) {
	encoded := cardtranscript.EncodeTool(cardtranscript.Tool{Name: "exec", Arguments: strings.Repeat("a", 1100), Output: strings.Repeat("界", 1100)})
	got := spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: encoded}})[0])
	command := strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 100)+"\n", 10), "\n")
	output := strings.TrimSuffix(strings.Repeat(strings.Repeat("界", 100)+"\n", 10), "\n")
	want := command + tooltext.Separator + output + "\n" + tooltext.Notice
	if got != want {
		t.Fatalf("independent default budgets: command present=%v output present=%v", strings.Contains(got, command), strings.Contains(got, output))
	}
}

func TestExecArgumentsRenderAsRichShellCodeSeparateFromOutput(t *testing.T) {
	rendered := renderedTool(t, cardtranscript.Tool{
		Name:      "exec",
		Arguments: "printf '<tag>&value\\n'",
		Output:    "plain <result>&",
		Encoding:  "text-v1",
	})
	if !strings.Contains(rendered, "```shell\nprintf '<tag>&value\\n'\n```") {
		t.Fatalf("exec command is not a shell code block: %q", rendered)
	}
	if !strings.Contains(rendered, tooltext.Separator+"plain &lt;result&gt;&amp;") {
		t.Fatalf("tool output is not a separate escaped block: %q", rendered)
	}
}

func TestExecArgumentsChooseFenceLongerThanCommandContent(t *testing.T) {
	rendered := renderedTool(t, cardtranscript.Tool{
		Name:      "exec",
		Arguments: "printf 'before'\n```\nprintf 'after'",
		Encoding:  "text-v1",
	})
	if !strings.Contains(rendered, "````shell\nprintf 'before'\n```\nprintf 'after'\n````") {
		t.Fatalf("embedded command fence broke shell block: %q", rendered)
	}
}

func TestEncodeAndRenderTwentyOneCommandAndOutputLines(t *testing.T) {
	encoded := cardtranscript.EncodeTool(cardtranscript.Tool{Name: "exec", Arguments: strings.Repeat("a", 2100), Output: strings.Repeat("界", 2100)})
	body := spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: encoded, CommandLines: 20, ToolLines: 20}})[0])
	command := strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 100)+"\n", 20), "\n")
	output := strings.TrimSuffix(strings.Repeat(strings.Repeat("界", 100)+"\n", 20), "\n")
	if body != command+tooltext.Separator+output+"\n"+tooltext.Notice {
		t.Fatal("encoding erased separately budgeted command or output")
	}
}

func TestToolAllBudgetsAfterStoredNativeContent(t *testing.T) {
	for _, choice := range []int{0, 3, 5, 10, 20, 40, -1, 7} {
		budget := choice
		if budget != 3 && budget != 5 && budget != 20 {
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

func TestToolCommandAndOutputBudgetsAreIndependent(t *testing.T) {
	choices := []int{0, 3, 5, 10, 20, -1, 7, 40}
	for _, command := range choices {
		for _, output := range choices {
			budget := func(n int) int {
				if n == 3 || n == 5 || n == 20 {
					return n
				}
				return 10
			}
			// Old readable JSON exercises renderer bounds independently of storage.
			raw, _ := json.Marshal(cardtranscript.Tool{Name: "exec", Encoding: "text-v1", Arguments: strings.Repeat("🙂", 2001), Output: strings.Repeat("界", 2001)})
			got := spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: string(raw), CommandLines: command, ToolLines: output}})[0])
			lines := func(glyph string, count int) string {
				return strings.TrimSuffix(strings.Repeat(strings.Repeat(glyph, 100)+"\n", count), "\n")
			}
			want := lines("🙂", budget(command)) + tooltext.Separator + lines("界", budget(output)) + "\n" + tooltext.Notice
			if got != want {
				t.Fatalf("command=%d output=%d independent postwrap budgets changed", command, output)
			}
		}
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

func TestPairedToolBudgetsKeepIdentityAndTransportNotice(t *testing.T) {
	command := "`\\n` <cmd>\r\n\n" + strings.Repeat("🙂", 101)
	output := "& <result>\n\n" + strings.Repeat("界", 101)
	call := cardtranscript.EncodeTool(cardtranscript.Tool{ID: "A", Name: "exec", Arguments: command, Status: "running"})
	result := cardtranscript.EncodeTool(cardtranscript.Tool{ID: "A", Output: output, Truncated: true, Status: "completed"})
	other := cardtranscript.EncodeTool(cardtranscript.Tool{ID: "B", Name: "other", Output: "unrelated"})
	blocks := []cardtranscript.Block{
		{Kind: "tool", Text: call, CommandLines: 3, ToolLines: 5},
		{Kind: "tool", Text: other, CommandLines: 20, ToolLines: 20},
		{Kind: "tool", Text: result, CommandLines: 3, ToolLines: 5},
	}
	got := cardtranscript.Render(blocks)
	want := "`\\n` <cmd>\n\n" + strings.Repeat("🙂", 100) + tooltext.Separator + "& <result>\n\n" + strings.Repeat("界", 100) + "\n界\n" + tooltext.Notice
	if len(got) != 2 || spoilerBody(t, got[0]) != want || spoilerBody(t, got[1]) != "unrelated" {
		t.Fatal("paired identity or independent explicit/wrapped lines changed")
	}
	if !strings.Contains(got[0], "```shell\n") || !strings.Contains(got[0], "<cmd>") || !strings.Contains(got[0], "✓ exec") {
		t.Fatal("command code block or completed status changed")
	}
	if blocks[0].Text != call || blocks[2].Text != result {
		t.Fatal("render mutated stored history")
	}
	// A transport loss still needs a notice when both retained fields fit.
	short := cardtranscript.EncodeTool(cardtranscript.Tool{Name: "exec", Arguments: "cmd", Output: "result", Truncated: true})
	if body := spoilerBody(t, cardtranscript.Render([]cardtranscript.Block{{Kind: "tool", Text: short, CommandLines: 3, ToolLines: 3}})[0]); body != "cmd"+tooltext.Separator+"result\n"+tooltext.Notice {
		t.Fatal("transport truncation notice lost")
	}
}

func TestStoredToolTwentyPlusTwentyUnicodeLinesSurvivePagination(t *testing.T) {
	for _, glyph := range []string{"🙂", "界", "<", "\x01"} {
		t.Run(glyph, func(t *testing.T) {
			encoded := cardtranscript.EncodeTool(cardtranscript.Tool{ID: strings.Repeat("i", 1024), Name: "exec", Arguments: strings.Repeat(glyph, 2000), Output: strings.Repeat(glyph, 2000)})
			if len(encoded) > 16384 || !json.Valid([]byte(encoded)) {
				t.Fatal("invalid persisted envelope")
			}
			blocks := cardtranscript.RenderBlocks([]cardtranscript.Block{{Kind: "tool", Text: encoded, CommandLines: 20, ToolLines: 20}})
			pages := cardtranscript.Paginate(blocks, 32)
			var restored strings.Builder
			for _, page := range pages {
				if len(page.Content) > 3000 || !utf8.ValidString(page.Content) {
					t.Fatal("invalid page")
				}
				restored.WriteString(spoilerBody(t, page.Content))
			}
			part := strings.TrimSuffix(strings.Repeat(strings.Repeat(glyph, 100)+"\n", 20), "\n")
			want := part + tooltext.Separator + part
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
	body = html.UnescapeString(strings.TrimSuffix(body, "\n\n</details>"))
	if strings.HasPrefix(body, "```shell\n") {
		body = strings.TrimPrefix(body, "```shell\n")
		if closing := strings.Index(body, "\n```"); closing >= 0 {
			body = body[:closing] + body[closing+4:]
		}
	}
	return body
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
