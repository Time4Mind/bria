package cardtranscript_test

import (
	"reflect"
	"strings"
	"testing"

	"bria/internal/cardtranscript"
)

func TestPaginateMarksOnlyActualTypedFinalStarts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		answer     string
		wantStarts []int
	}{
		{"short", "current answer", []int{2, 4}},
		{"long", strings.Repeat("a", 6500), []int{2, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages := cardtranscript.Paginate(cardtranscript.RenderBlocks([]cardtranscript.Block{
				{Kind: "prompt", Text: "first prompt"},
				{Kind: "final", Text: "previous final"},
				{Kind: "prompt", Text: "current prompt"},
				{Kind: "commentary", Text: "final response starts here - not a typed final"},
				{Kind: "final", Text: tc.answer},
				{Kind: "final", Text: " \n"},
				{Kind: "commentary", Text: "trailing status"},
			}), 32)
			var starts []int
			var answer strings.Builder
			for i, page := range pages {
				if page.FinalStart {
					starts = append(starts, i+1)
				}
				if i >= 3 && i < len(pages)-1 {
					answer.WriteString(page.Content)
				}
			}
			if !reflect.DeepEqual(starts, tc.wantStarts) {
				t.Fatalf("typed final start pages=%v want=%v", starts, tc.wantStarts)
			}
			if answer.String() != tc.answer {
				t.Fatal("final boundary metadata changed answer bytes")
			}
		})
	}
}

func TestFinalStartMetadataNeverRelabelsRetainedContinuation(t *testing.T) {
	blocks := []cardtranscript.Block{{Kind: "final", Text: strings.Repeat("x", 6500)}}
	for _, limit := range []int{2, 3} {
		pages := cardtranscript.Paginate(blocks, limit)
		if len(pages) != limit {
			t.Fatalf("retention=%d pages=%d", limit, len(pages))
		}
		for i, page := range pages {
			want := limit == 3 && i == 0
			if page.FinalStart != want {
				t.Fatalf("limit=%d page=%d finalStart=%t want=%t", limit, i+1, page.FinalStart, want)
			}
		}
	}
}

func TestProtocolSizedFinalCanLoseStartAtExistingPageLimit(t *testing.T) {
	// Source bytes are below the runtime limit, but independent fences produce
	// more than 32 renderable pages. Retention policy is intentionally unchanged.
	answer := strings.Repeat("```text\n"+strings.Repeat("x", 80)+"\n```\n", 40)
	if len(answer) >= 32<<10 {
		t.Fatal("fixture exceeds runtime final limit")
	}
	blocks := []cardtranscript.Block{{Kind: "final", Text: answer}}
	full := cardtranscript.Paginate(blocks, 64)
	retained := cardtranscript.Paginate(blocks, 32)
	if len(full) != 40 || !full[0].FinalStart || len(retained) != 32 {
		t.Fatalf("full=%d retained=%d", len(full), len(retained))
	}
	for _, page := range retained {
		if page.FinalStart {
			t.Fatal("retained continuation falsely claims final start")
		}
	}
}

func TestProtocolSizedFinalCanExceedDurablePageHardLimit(t *testing.T) {
	answer := strings.Repeat("```text\nx\n```\n", 600)
	if len(answer) >= 32<<10 {
		t.Fatal("fixture exceeds runtime final limit")
	}
	pages := cardtranscript.Paginate([]cardtranscript.Block{{Kind: "final", Text: answer}}, 601)
	if len(pages) != 600 || !pages[0].FinalStart {
		t.Fatalf("short fenced final pages=%d want=600", len(pages))
	}
}

func TestExactFinalContinuationPreservesOnlyFirstChunkStart(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		blocks := []cardtranscript.Block{
			{Kind: "final", Text: strings.Repeat("a", 16<<10)},
			{Kind: "final", Text: strings.Repeat("b", 16<<10), FinalContinuation: continuation},
		}
		rendered := cardtranscript.RenderBlocks(blocks)
		if len(rendered) != 2 || rendered[1].FinalContinuation != continuation {
			t.Fatalf("render lost exact continuation=%t", continuation)
		}
		pages := cardtranscript.Paginate(rendered, 32)
		var starts []int
		var body strings.Builder
		for index, page := range pages {
			if page.FinalStart {
				starts = append(starts, index+1)
			}
			body.WriteString(page.Content)
		}
		want := []int{1, 7}
		if continuation {
			want = []int{1}
		}
		if !reflect.DeepEqual(starts, want) {
			t.Fatalf("continuation=%t start pages=%v want=%v", continuation, starts, want)
		}
		if body.String() != blocks[0].Text+blocks[1].Text {
			t.Fatal("continuation metadata changed final chunks")
		}
	}
}
