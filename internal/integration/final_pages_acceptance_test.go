package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

func TestFinalPageSurvivesStoreReopenAndTechnicalDisplayToggle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "intent", "local", domain.ProviderCodex, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, starting.ID(), "prompt-1", "Вопрос"); err != nil {
		t.Fatal(err)
	}
	for _, block := range []cardtranscript.Block{
		{Kind: "tool", Text: cardtranscript.EncodeTool(cardtranscript.Tool{ID: "t", Name: "read", Arguments: "README.md", Status: "in_progress"})},
		{Kind: "commentary", Text: "Проверяю"},
		{Kind: "tool", Text: cardtranscript.EncodeTool(cardtranscript.Tool{ID: "t", Name: "read", Output: "TOOL_RESULT", Status: "completed"})},
		{Kind: "final", Text: "Финальный ответ"},
	} {
		if err := store.InsertCardTypedHistoryAfterPrompt(ctx, starting.ID(), "prompt-1", block.Text, block.Kind); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	prefs := settingscomposition.Preferences{Store: settings.NewMemoryStore()}
	c, err := telegramcontroller.New(42, 42, "local", staticCreator{session: starting}, reopened, &capturingSubmitter{calls: make(chan submittedTurn, 1)}, discardNotifier{}, telegramcontroller.Options{UIState: reopened, Settings: prefs})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	for _, show := range []bool{false, true, false} {
		snapshot, err := prefs.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.ShowTechnicalActions != show {
			if err := prefs.ToggleTechnicalActions(ctx); err != nil {
				t.Fatal(err)
			}
		}
		result, err := c.ProjectCurrent(ctx, starting.ID())
		if err != nil || result.Card == nil {
			t.Fatalf("projection: %+v %v", result, err)
		}
		pages := result.Card.Pages
		if len(pages) != 2 || pages[1].Content != "Финальный ответ" || strings.Contains(pages[0].Content, "TOOL_RESULT") != show {
			t.Fatalf("show=%t pages=%+v", show, pages)
		}
	}
}
