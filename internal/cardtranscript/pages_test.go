package cardtranscript_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"bria/internal/cardtranscript"
)

func TestFinalPageBoundariesIgnoreEmptyBlocksAndPreserveAnchors(t *testing.T) {
	for _, test := range []struct {
		name   string
		blocks []cardtranscript.Block
		want   []cardtranscript.Page
	}{
		{"empty", nil, []cardtranscript.Page{{Anchors: []string{"empty"}}}},
		{"first final", []cardtranscript.Block{{Kind: "final", Text: "done"}}, []cardtranscript.Page{{Content: "done", Anchors: []string{"history:1"}}}},
		{"empty final", []cardtranscript.Block{{Text: "before"}, {Kind: "final", Text: " \n"}, {Text: "after"}}, []cardtranscript.Page{{Content: "before" + cardtranscript.Separator + "after", Anchors: []string{"history:1", "history:2"}}}},
		{"no text heuristic", []cardtranscript.Block{{Text: "final"}, {Text: "answer"}}, []cardtranscript.Page{{Content: "final" + cardtranscript.Separator + "answer", Anchors: []string{"history:1", "history:2"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pages := cardtranscript.Paginate(cardtranscript.RenderBlocks(test.blocks), 32)
			if !reflect.DeepEqual(pages, test.want) {
				t.Fatalf("pages=%#v want=%#v", pages, test.want)
			}
		})
	}
}

func TestDedicatedFinalPagesKeepConfiguredLatestWindow(t *testing.T) {
	var blocks []cardtranscript.Block
	for i := 1; i <= 40; i++ {
		blocks = append(blocks, cardtranscript.Block{Kind: "final", Text: fmt.Sprintf("answer %d", i)})
	}
	pages := cardtranscript.Paginate(cardtranscript.RenderBlocks(blocks), 32)
	if len(pages) != 32 || pages[0].Content != "answer 9" || pages[0].Anchors[0] != "history:9" || pages[31].Content != "answer 40" {
		t.Fatalf("latest window=%#v", pages)
	}
}

func TestLongFencedFinalKeepsRenderabilityAndDedicatedContinuation(t *testing.T) {
	code := strings.Repeat("println(42)\n", 600)
	pages := cardtranscript.Paginate(cardtranscript.RenderBlocks([]cardtranscript.Block{
		{Text: "before"}, {Kind: "final", Text: "```go\n" + code + "```"}, {Text: "after"},
	}), 32)
	if len(pages) < 5 || pages[0].Content != "before" || pages[len(pages)-1].Content != "after" {
		t.Fatal("missing dedicated boundaries")
	}
	var reconstructed strings.Builder
	for _, page := range pages[1 : len(pages)-1] {
		if len(page.Content) > 3000 || !strings.HasPrefix(page.Content, "```go\n") || !strings.HasSuffix(page.Content, "```") {
			t.Fatalf("invalid standalone code page: %q", page.Content)
		}
		body := strings.TrimSuffix(strings.TrimPrefix(page.Content, "```go\n"), "```")
		// Split adds one closing newline when a fence continues; source lines
		// in this fixture already end in newline.
		if strings.HasSuffix(body, "\n\n") {
			body = strings.TrimSuffix(body, "\n")
		}
		reconstructed.WriteString(body)
	}
	if reconstructed.String() != code {
		t.Fatalf("code changed across pages: %d bytes want %d", reconstructed.Len(), len(code))
	}
}

func TestLineSplitFinalNeverInsertsInterEventSeparatorsInsideAnswer(t *testing.T) {
	answer := strings.TrimSpace(strings.Repeat(strings.Repeat("a", 1461)+"\n", 4))
	pages := cardtranscript.Paginate(cardtranscript.RenderBlocks([]cardtranscript.Block{{Kind: "final", Text: answer}}), 32)
	var got strings.Builder
	for _, page := range pages {
		got.WriteString(page.Content)
	}
	if got.String() != answer {
		t.Fatalf("final text changed: got %d bytes want %d", got.Len(), len(answer))
	}
}
