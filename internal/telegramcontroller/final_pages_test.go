package telegramcontroller_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

type finalPageState struct {
	projectionUIState
	blocks []cardtranscript.Block
}

func (s *finalPageState) AppendCardTypedHistory(context.Context, domain.SessionID, string, string) error {
	return nil
}
func (s *finalPageState) LoadCardTranscript(_ context.Context, _ domain.SessionID, show bool) ([]cardtranscript.Block, error) {
	var result []cardtranscript.Block
	for _, block := range s.blocks {
		if show || block.Kind != "tool" {
			result = append(result, block)
		}
	}
	return result, nil
}

func TestFinalStartsDedicatedPageInCurrentAndCompletionProjection(t *testing.T) {
	for _, answer := range []string{"Короткий ответ", strings.Repeat("я", 3500)} {
		t.Run(stringLengthName(answer), func(t *testing.T) {
			ctx := context.Background()
			ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider", 1)
			state := &finalPageState{blocks: []cardtranscript.Block{
				{Kind: "prompt", Text: "Вопрос"}, {Kind: "commentary", Text: "Проверяю"},
				{Kind: "final", Text: answer}, {Kind: "prompt", Text: "Следующий вопрос"},
				{Kind: "final", Text: "Второй ответ"}, {Kind: "final", Text: "Третий ответ"},
			}}
			c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: state})
			defer c.Close(ctx)
			current, err := c.ProjectCurrent(ctx, ready.ID())
			if err != nil || current.Card == nil {
				t.Fatalf("current: %+v %v", current, err)
			}
			completion, _, err := c.ProjectCompletion(ctx, ready.ID())
			if err != nil {
				t.Fatal(err)
			}
			for name, card := range map[string]telegramcontroller.SemanticCard{"current": *current.Card, "completion": completion} {
				pages := card.Pages
				if len(pages) < 5 {
					t.Fatalf("%s: final was packed with other events: %d pages", name, len(pages))
				}
				if pages[0].Content != "Вопрос"+cardtranscript.Separator+"Проверяю" {
					t.Fatalf("%s: preceding page=%q", name, pages[0].Content)
				}
				var got strings.Builder
				for _, page := range pages[1 : len(pages)-3] {
					if len(page.Content) > 3000 || !utf8.ValidString(page.Content) {
						t.Fatal("invalid page budget/UTF-8")
					}
					got.WriteString(page.Content)
				}
				if got.String() != answer {
					t.Fatalf("%s: isolated final changed/mixed: %q", name, got.String())
				}
				for i, want := range []string{"Следующий вопрос", "Второй ответ", "Третий ответ"} {
					if pages[len(pages)-3+i].Content != want {
						t.Fatalf("%s: following page=%q want=%q", name, pages[len(pages)-3+i].Content, want)
					}
				}
			}
		})
	}
}

func stringLengthName(s string) string {
	if len(s) > 3000 {
		return "long_UTF8"
	}
	return "short"
}
