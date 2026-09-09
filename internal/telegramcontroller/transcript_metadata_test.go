package telegramcontroller

import (
	"context"
	"strings"
	"testing"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
)

type metadataHistoryStore struct{ blocks []cardtranscript.Block }

func (s *metadataHistoryStore) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (s *metadataHistoryStore) AppendCardTypedHistory(_ context.Context, _ domain.SessionID, text, kind string) error {
	s.blocks = append(s.blocks, cardtranscript.Block{Text: text, Kind: kind})
	return nil
}
func (s *metadataHistoryStore) LoadCardTranscript(context.Context, domain.SessionID, bool) ([]cardtranscript.Block, error) {
	return s.blocks, nil
}

func TestRuntimeMetadataReachesPersistedTranscriptAndPairedSpoilers(t *testing.T) {
	ctx := context.Background()
	id := domain.SessionID("test")
	store := &metadataHistoryStore{}
	c := &Controller{uiState: store, history: map[domain.SessionID][]string{}}
	for _, event := range []sessionruntime.TurnEvent{
		{Kind: sessionruntime.EventThinking, Text: "inspect the tree"},
		{Kind: sessionruntime.EventTool, Text: "exec", Metadata: &runtimeprotocol.EventMetadata{ItemID: "a", Name: "exec", Arguments: "ls", Status: "in_progress"}},
		{Kind: sessionruntime.EventTool, Text: "read", Metadata: &runtimeprotocol.EventMetadata{ItemID: "b", Name: "read", Arguments: "README.md", Status: "in_progress"}},
		{Kind: sessionruntime.EventTool, Text: "exec result", Metadata: &runtimeprotocol.EventMetadata{ItemID: "a", Result: "file.go", Status: "completed"}},
	} {
		c.appendRuntimeHistory(ctx, id, event)
	}
	// A fresh controller must retain types and pair the correct durable call.
	restarted := &Controller{uiState: store}
	items, err := restarted.displayHistory(ctx, id, nil)
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if !strings.Contains(items[0].Text, "<summary>∴ thinking</summary>") || !strings.Contains(items[1].Text, "✓ exec</summary>\n\n```shell\nls\n```\n\n---\n\nfile.go") || strings.Contains(items[2].Text, "file.go") {
		t.Fatalf("metadata lost/mispaired: %#v", items)
	}
	pages := cardtranscript.Paginate(items, 64)
	if len(pages) != 1 || strings.Count(pages[0].Content, cardtranscript.Separator) != 2 {
		t.Fatalf("incorrect block separators: %#v", pages)
	}
}
