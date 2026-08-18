package application_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestCardRendererKeepsFixedLatestPageWindow(t *testing.T) {
	render := func(count int) application.CardEventPages {
		events := make([]application.CardEvent, 0, count)
		for index := 1; index <= count; index++ {
			events = append(events, application.CardEvent{
				ID: fmt.Sprintf("event-%d", index), Kind: application.CardEventAssistantText,
				Text: fmt.Sprintf("answer-%d", index), PageBreak: true,
			})
		}
		return application.RenderCardEventPages(
			domain.DefaultUserPreferences(), events,
			application.CardRenderOptions{MaxPages: 3},
		)
	}
	first := render(5)
	second := render(6)
	for name, result := range map[string]application.CardEventPages{
		"first": first, "second": second,
	} {
		if len(result.Pages) != 3 || result.Latest.Number != 3 || result.Latest.Count != 3 {
			t.Fatalf("%s page window = %#v", name, result)
		}
		if result.LatestResponseStart.Number != 3 {
			t.Fatalf("%s latest response start = %#v", name, result.LatestResponseStart)
		}
		for index, page := range result.Pages {
			if page.Number != index+1 || page.Count != 3 {
				t.Fatalf("%s page %d metadata = %#v", name, index, page)
			}
		}
	}
	if !strings.Contains(first.Pages[0].RichMarkdown, "answer-3") ||
		!strings.Contains(second.Pages[0].RichMarkdown, "answer-4") ||
		!strings.Contains(second.Latest.RichMarkdown, "answer-6") {
		t.Fatalf("page window did not advance by content: first=%#v second=%#v", first, second)
	}
}
