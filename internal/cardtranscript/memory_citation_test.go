package cardtranscript

import (
	"strings"
	"testing"
)

const persistedMemoryCitation = `<oai-mem-citation>
<citation_entries>
MEMORY.md:12-14|note=[session context]
</citation_entries>
<rollout_ids>
</rollout_ids>
</oai-mem-citation>`

func TestRenderBlocksCleansPersistedTypedFinalOnly(t *testing.T) {
	blocks := RenderBlocks([]Block{
		{Kind: "final", Text: "Старый ответ.\n\n" + persistedMemoryCitation, FinalOperationID: "final-1"},
		{Kind: "tool", Text: `tool read MEMORY.md ` + persistedMemoryCitation},
		{Kind: "commentary", Text: "Обычное упоминание MEMORY.md"},
	})
	if len(blocks) != 3 || blocks[0].Text != "Старый ответ." || blocks[0].FinalOperationID != "final-1" {
		t.Fatalf("persisted final not cleaned safely: %#v", blocks)
	}
	if !strings.Contains(blocks[1].Text, "MEMORY.md") || !strings.Contains(blocks[2].Text, "MEMORY.md") {
		t.Fatalf("non-final memory text changed: %#v", blocks)
	}
}

func TestRenderBlocksKeepsEmbeddedOrMalformedCitation(t *testing.T) {
	for _, text := range []string{
		persistedMemoryCitation + "\nvisible",
		strings.Replace(persistedMemoryCitation, "</citation_entries>", "", 1),
	} {
		got := RenderBlocks([]Block{{Kind: "final", Text: text}})
		if len(got) != 1 || !strings.Contains(got[0].Text, "oai-mem-citation") {
			t.Fatalf("invalid envelope removed: %#v", got)
		}
	}
}

func TestRenderBlocksCleansCitationSplitAcrossPersistedFinalChunks(t *testing.T) {
	text := "Длинный сохранённый ответ.\n\n" + persistedMemoryCitation
	cut := strings.Index(text, "<rollout_ids>") + 5
	blocks := RenderBlocks([]Block{
		{Kind: "final", Text: text[:cut], FinalOperationID: "final-split"},
		{Kind: "final", Text: text[cut:], FinalContinuation: true, FinalOperationID: "final-split"},
	})
	if len(blocks) != 1 || blocks[0].Text != "Длинный сохранённый ответ." || blocks[0].FinalOperationID != "final-split" {
		t.Fatalf("split persisted final not cleaned: %#v", blocks)
	}
}

func TestRenderBlocksKeepsCitationAtEndOfIntermediateFinalChunk(t *testing.T) {
	blocks := RenderBlocks([]Block{
		{Kind: "final", Text: "Начало ответа.\n\n" + persistedMemoryCitation, FinalOperationID: "final-with-continuation"},
		{Kind: "final", Text: "\nПродолжение ответа.", FinalContinuation: true, FinalOperationID: "final-with-continuation"},
	})
	if len(blocks) != 2 {
		t.Fatalf("joined final chunks changed shape: %#v", blocks)
	}
	if !strings.Contains(blocks[0].Text, "oai-mem-citation") || blocks[1].Text != "Продолжение ответа." {
		t.Fatalf("intermediate citation envelope was stripped: %#v", blocks)
	}
}
