package cardtranscript

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func toolBlock(tool Tool) Block {
	encoded, _ := json.Marshal(tool)
	return Block{Kind: "tool", Text: string(encoded)}
}

func TestToolResultMergesWithExactCallAndPreservesOtherBlocks(t *testing.T) {
	blocks := Render([]Block{
		{Kind: "prompt", Text: "👨‍💻 inspect files"},
		toolBlock(Tool{ID: "a", Name: "exec", Arguments: "ls", Status: "running"}),
		toolBlock(Tool{ID: "b", Name: "read", Arguments: "file"}),
		{Kind: "commentary", Text: "Checking **files**"},
		toolBlock(Tool{ID: "a", Output: "one\ntwo", Status: "completed"}),
		{Kind: "final", Text: "Done"},
	})
	if len(blocks) != 5 {
		t.Fatalf("rendered blocks: %#v", blocks)
	}
	if !strings.Contains(blocks[1], "✓ exec</summary>\n\n```shell\nls\n```\n\n---\n\none\ntwo") || strings.Contains(blocks[2], "one") {
		t.Fatalf("result merged incorrectly: %#v", blocks)
	}
	if blocks[0] != "👨‍💻 inspect files" || blocks[3] != "Checking **files**" || blocks[4] != "Done" {
		t.Fatalf("surrounding content changed: %#v", blocks)
	}
}

func TestToolSpoilerDecodesTransportEscapes(t *testing.T) {
	blocks := Render([]Block{toolBlock(Tool{
		Name:      "exec",
		Arguments: `printf foo\\nbar \/tmp`,
		Output:    `first\\nsecond`,
		Status:    "completed",
	})})
	if !strings.Contains(blocks[0], "foo\nbar /tmp") || !strings.Contains(blocks[0], "first\nsecond") {
		t.Fatalf("spoiler keeps transport escaping: %q", blocks[0])
	}
	if strings.Contains(blocks[0], `\\n`) || strings.Contains(blocks[0], `\/`) {
		t.Fatalf("escaped sequences leaked into spoiler: %q", blocks[0])
	}
}

func TestSpoilersEmptyToolAndProviderHTML(t *testing.T) {
	blocks := Render([]Block{{Kind: "thinking", Text: "check <x>"}, toolBlock(Tool{Name: "exec", Status: "running"}), {Text: "<details>user text</details>"}})
	if !strings.Contains(blocks[0], "<summary>∴ thinking</summary>") || !strings.Contains(blocks[0], "&lt;x&gt;") {
		t.Fatal(blocks)
	}
	if blocks[1] != "… exec" || strings.Contains(blocks[2], "<details>") {
		t.Fatal(blocks)
	}
}

func TestPromptTruncationDoesNotChangeSourceOrStatus(t *testing.T) {
	for _, glyph := range []string{"🙋‍♂", "👨‍💻", "🙅‍♂"} {
		original := Block{Kind: "prompt", Text: glyph + " " + strings.Repeat("я", 300)}
		got := Render([]Block{original})[0]
		if utf8.RuneCountInString(got) != 200 || !strings.HasPrefix(got, glyph) || len(original.Text) < 600 {
			t.Fatal(got)
		}
	}
}

func TestPagesKeepSpoilerAndCodeFencesBalanced(t *testing.T) {
	for _, original := range []string{details("tool", strings.Repeat("&lt;я&gt;\n", 1000)), NormalizeMarkdown("```go\n" + strings.Repeat("fmt.Println(1)\n", 500))} {
		parts := Split(original, 3000)
		if len(parts) < 2 {
			t.Fatal("not split")
		}
		for _, part := range parts {
			if len(part) > 3000 || !utf8.ValidString(part) {
				t.Fatal("invalid bounded page")
			}
			if strings.HasPrefix(original, "<details>") && (!strings.HasPrefix(part, "<details>") || !strings.HasSuffix(part, "</details>")) {
				t.Fatal(part)
			}
			if strings.Contains(original, "```") && strings.Count(part, "```")%2 != 0 {
				t.Fatal("unbalanced fence", part)
			}
		}
	}
}

func TestNormalizeMarkdownSeparatesTableAndClosesFence(t *testing.T) {
	got := NormalizeMarkdown("result\n| a | b |\n|---|---|\n```go\n<x>")
	if !strings.Contains(got, "result\n\n| a") || !strings.HasSuffix(got, "<x>\n```") {
		t.Fatal(got)
	}
}

func TestEncodedToolFitsPersistedEntryAndPreservesIdentity(t *testing.T) {
	id := strings.Repeat("identity", 300)
	encoded := EncodeTool(Tool{ID: id, Name: "exec", Arguments: strings.Repeat("<\n", 10000), Output: strings.Repeat("я\t", 10000)})
	var decoded Tool
	// Arguments consume the shared forty-line budget; omitted output must be
	// explicit rather than preserving separate per-field byte allowances.
	if len(encoded) > 16384 || json.Unmarshal([]byte(encoded), &decoded) != nil || !strings.HasPrefix(decoded.ID, "sha256:") || decoded.Arguments == "" || !decoded.Truncated {
		t.Fatalf("invalid bounded tool: bytes=%d value=%#v", len(encoded), decoded)
	}
	var same Tool
	_ = json.Unmarshal([]byte(EncodeTool(Tool{ID: id, Output: "result"})), &same)
	if decoded.ID != same.ID {
		t.Fatal("bounded identity changed across updates")
	}
}
