package cardtranscript_test

import (
	"strings"
	"testing"

	"bria/internal/cardtranscript"
)

func TestFinalIdentitySurvivesRenderingChunksAndRetention(t *testing.T) {
	blocks := []cardtranscript.Block{
		{Kind: "prompt", Text: "A:final", FinalOperationID: "not-a-final"},
		{Kind: "final", Text: strings.Repeat("a", 16<<10), FinalOperationID: "A:final"},
		{Kind: "final", Text: strings.Repeat("b", 16<<10), FinalOperationID: "A:final", FinalContinuation: true},
		{Kind: "final", Text: "B result", FinalOperationID: "B:final"},
	}
	pages := cardtranscript.Paginate(cardtranscript.RenderBlocks(blocks), 32)
	if len(pages) != 14 {
		t.Fatalf("pages=%d want=14", len(pages))
	}
	if pages[0].FinalOperationID != "" {
		t.Fatal("non-final acquired operation identity")
	}
	for index, page := range pages[1:13] {
		if page.FinalOperationID != "A:final" || page.FinalStart != (index == 0) {
			t.Fatalf("A page=%d operation=%q start=%t", index+1, page.FinalOperationID, page.FinalStart)
		}
	}
	if pages[13].FinalOperationID != "B:final" || !pages[13].FinalStart {
		t.Fatal("B final identity lost")
	}
	retained := cardtranscript.Paginate(cardtranscript.RenderBlocks(blocks), 3)
	if retained[0].FinalOperationID != "A:final" || retained[0].FinalStart {
		t.Fatal("retained A continuation lost identity or invented start")
	}
}
