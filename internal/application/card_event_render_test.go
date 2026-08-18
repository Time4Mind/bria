package application_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestCardRendererMatchesCCBotEventLayoutAndFoldsToolResult(t *testing.T) {
	started := cardTime(10, 0)
	resultAt := cardTime(10, 2)
	events := []application.CardEvent{
		{Kind: application.CardEventUserText, Text: "run checks", StartedAt: started},
		{Kind: application.CardEventThinking, Body: "I should inspect it", StartedAt: started, CompletedAt: &resultAt},
		{
			Kind: application.CardEventToolCall, Text: "Bash", Body: "```bash\ngo test ./...\n```",
			ToolUseID: "tool-1", StartedAt: cardTime(10, 1),
		},
		{
			Kind: application.CardEventToolResult, Text: "Bash · passed", Body: "ok",
			ToolUseID: "tool-1", StartedAt: resultAt,
		},
		{Kind: application.CardEventAssistantText, Text: "All tests pass.", StartedAt: cardTime(10, 3)},
	}

	got := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), events, fixedCardOptions(3500),
	)
	want := strings.Join([]string{
		"👤 run checks",
		"<details><summary>∴ thinking · 10:00</summary>\n\nI should inspect it\n\n</details>",
		"<details><summary>✓ Bash · 10:01</summary>\n\n```bash\ngo test ./...\n```\n\n---\n\nok\n\n</details>",
		"All tests pass.",
	}, "\n\n\u00a0\n\n")
	if got.Latest.RichMarkdown != want {
		t.Fatalf("rendered page mismatch\n--- got ---\n%s\n--- want ---\n%s", got.Latest.RichMarkdown, want)
	}
	if len(got.Pages) != 1 || got.Latest.Number != 1 || got.Latest.Count != 1 {
		t.Fatalf("page metadata = %#v", got)
	}
}

func TestCardRendererTechnicalVisibilityIsIndependent(t *testing.T) {
	tests := []struct {
		name      string
		hide      []domain.CardEventType
		want      []string
		doNotWant []string
	}{
		{
			name:      "result hidden keeps completed call arguments",
			hide:      []domain.CardEventType{domain.CardEventToolResult},
			want:      []string{"✓ Bash · 10:00", "secret-command", "answer"},
			doNotWant: []string{"command-output", "Bash · done"},
		},
		{
			name:      "call hidden leaves standalone result",
			hide:      []domain.CardEventType{domain.CardEventToolCall},
			want:      []string{"✓ Bash · 10:01", "command-output", "answer"},
			doNotWant: []string{"secret-command"},
		},
		{
			name:      "thinking hidden leaves no shell",
			hide:      []domain.CardEventType{domain.CardEventThinking},
			want:      []string{"Bash · 10:00", "secret-command", "command-output", "answer"},
			doNotWant: []string{"thinking", "private-reasoning"},
		},
		{
			name: "all hidden leaves conversation only",
			hide: []domain.CardEventType{
				domain.CardEventToolCall, domain.CardEventToolResult, domain.CardEventThinking,
			},
			want: []string{"👤 request", "answer"},
			doNotWant: []string{
				"<details>", "thinking", "Bash", "secret-command", "command-output",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preferences := domain.DefaultUserPreferences()
			for _, eventType := range test.hide {
				if err := preferences.SetCardEventVisibility(eventType, false); err != nil {
					t.Fatal(err)
				}
			}
			text := application.RenderCardEventPages(
				preferences, visibilityEvents(), fixedCardOptions(3500),
			).Latest.RichMarkdown
			for _, value := range test.want {
				if !strings.Contains(text, value) {
					t.Errorf("missing %q in %q", value, text)
				}
			}
			for _, value := range test.doNotWant {
				if strings.Contains(text, value) {
					t.Errorf("unexpected %q in %q", value, text)
				}
			}
		})
	}
}

func TestVisibleToolResultKeepsNameFromHiddenRealisticCall(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	if err := preferences.SetCardEventVisibility(domain.CardEventToolCall, false); err != nil {
		t.Fatal(err)
	}
	text := application.RenderCardEventPages(preferences, []application.CardEvent{
		{Kind: application.CardEventToolCall, Text: "Bash", ToolUseID: "tool-1"},
		{
			Kind: application.CardEventToolResult, Text: "Tool result", Body: "ok",
			ToolUseID: "tool-1",
		},
	}, fixedCardOptions(3500)).Latest.RichMarkdown
	if !strings.Contains(text, "✓ Bash") || strings.Contains(text, "Tool result") {
		t.Fatalf("hidden-call result lost tool identity: %q", text)
	}
}

func TestCardRendererUsesInFlightGlyphAndElapsedTime(t *testing.T) {
	started := cardTime(10, 0)
	events := []application.CardEvent{
		{Kind: application.CardEventToolCall, Text: "Read", StartedAt: started},
		{Kind: application.CardEventThinking, Body: "working", StartedAt: cardTime(10, 1)},
	}
	text := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), events, application.CardRenderOptions{
			Now: cardTime(10, 2).Add(5 * time.Second), Location: time.UTC,
		},
	).Latest.RichMarkdown
	if !strings.Contains(text, "✓ Read · 10:00") ||
		!strings.Contains(text, "∴ thinking · ⏳ 1:05") {
		t.Fatalf("unexpected in-flight rendering: %q", text)
	}
}

func TestCardRendererPaginationIsBoundedDeterministicAndLatest(t *testing.T) {
	text := strings.Repeat("first sentence. ", 25) + "\n\n" + strings.Repeat("last paragraph ", 25)
	events := []application.CardEvent{{Kind: application.CardEventAssistantText, Text: text}}
	first := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), events, fixedCardOptions(90),
	)
	second := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), events, fixedCardOptions(90),
	)
	if len(first.Pages) < 2 || len(first.Pages) != len(second.Pages) {
		t.Fatalf("pages = %d and %d", len(first.Pages), len(second.Pages))
	}
	for index, page := range first.Pages {
		if utf8.RuneCountInString(page.RichMarkdown) > 90 {
			t.Errorf("page %d has %d runes", index, utf8.RuneCountInString(page.RichMarkdown))
		}
		if page.RichMarkdown != second.Pages[index].RichMarkdown ||
			page.Number != index+1 || page.Count != len(first.Pages) {
			t.Errorf("page %d is not deterministic: %#v / %#v", index, page, second.Pages[index])
		}
	}
	if first.Latest != first.Pages[len(first.Pages)-1] {
		t.Fatalf("latest page = %#v", first.Latest)
	}
}

func TestCardRendererTracksBeginningOfLatestResponse(t *testing.T) {
	result := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), []application.CardEvent{
			{Kind: application.CardEventAssistantText, Text: "older answer", PageBreak: true},
			{Kind: application.CardEventAssistantText,
				Text:      "LATEST RESPONSE START " + strings.Repeat("middle ", 30) + "LATEST RESPONSE END",
				PageBreak: true},
		}, fixedCardOptions(80),
	)
	if result.LatestResponseStart.Number < 2 ||
		result.LatestResponseStart.Number >= result.Latest.Number {
		t.Fatalf("latest response pages = start %#v, latest %#v",
			result.LatestResponseStart, result.Latest)
	}
	if !strings.Contains(result.LatestResponseStart.RichMarkdown, "LATEST RESPONSE START") ||
		strings.Contains(result.LatestResponseStart.RichMarkdown, "LATEST RESPONSE END") {
		t.Fatalf("latest response start page = %q", result.LatestResponseStart.RichMarkdown)
	}
}

func TestCardRendererBoundsMultibytePagesForTelegramFallback(t *testing.T) {
	events := []application.CardEvent{{
		Kind: application.CardEventAssistantText,
		Text: strings.Repeat("длинный русский ответ ", 400),
	}}
	result := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), events, application.CardRenderOptions{},
	)
	if len(result.Pages) < 2 {
		t.Fatalf("multibyte answer was not paginated: %d pages", len(result.Pages))
	}
	for index, page := range result.Pages {
		if len(page.RichMarkdown) > application.DefaultCardPageBytes {
			t.Fatalf("page %d has %d encoded bytes", index+1, len(page.RichMarkdown))
		}
		if !utf8.ValidString(page.RichMarkdown) {
			t.Fatalf("page %d is not valid UTF-8", index+1)
		}
	}
}

func TestCardRendererBoundsTechnicalBlockWithoutBreakingDetails(t *testing.T) {
	events := []application.CardEvent{{
		Kind: application.CardEventToolCall, Text: "Read", Body: strings.Repeat("long body ", 100),
		StartedAt: cardTime(10, 0),
	}}
	page := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), events, fixedCardOptions(160),
	).Latest.RichMarkdown
	if utf8.RuneCountInString(page) > 160 {
		t.Fatalf("technical page has %d runes", utf8.RuneCountInString(page))
	}
	if !strings.HasPrefix(page, "<details><summary>▷ Read · ⏳ 2:00</summary>") ||
		!strings.HasSuffix(page, "</details>") {
		t.Fatalf("broken expandable block: %q", page)
	}
}

func TestCardRendererBoundsExpandableSummaryLikeCCBot(t *testing.T) {
	page := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), []application.CardEvent{{
			Kind: application.CardEventToolCall,
			Text: strings.Repeat("long-tool-name-", 10), Body: "details",
		}}, fixedCardOptions(3500),
	).Latest.RichMarkdown
	summaryEnd := strings.Index(page, "</summary>")
	if summaryEnd < 0 {
		t.Fatalf("details summary missing: %q", page)
	}
	summary := strings.TrimPrefix(page[:summaryEnd], "<details><summary>")
	if utf8.RuneCountInString(summary) > 64 || !strings.HasSuffix(summary, "…") {
		t.Fatalf("summary was not bounded to one compact line: %q", summary)
	}
}

func TestCardRendererLimitsTechnicalLinesAndKeepsFinalAnswer(t *testing.T) {
	lines := make([]string, 25)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%02d", index+1)
	}
	preferences := domain.DefaultUserPreferences()
	preferences.ToolOutputLines = 15
	page := application.RenderCardEventPages(preferences, []application.CardEvent{
		{Kind: application.CardEventToolResult, Text: "Bash", Body: strings.Join(lines, "\n")},
		{Kind: application.CardEventAssistantText, Text: "FINAL ANSWER"},
	}, fixedCardOptions(3500)).Latest.RichMarkdown
	for _, want := range []string{"line-01", "line-15", "… (+10 more lines)", "FINAL ANSWER"} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q in %q", want, page)
		}
	}
	if strings.Contains(page, "line-16") {
		t.Fatalf("technical output exceeded configured limit: %q", page)
	}
}

func TestCardRendererKeepsFenceWhenLimitingToolCommand(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	preferences.ToolOutputLines = 2
	page := application.RenderCardEventPages(preferences, []application.CardEvent{{
		Kind: application.CardEventToolCall,
		Text: "Bash",
		Body: "```bash\none\ntwo\nthree\n```",
	}}, fixedCardOptions(3500)).Latest.RichMarkdown
	for _, want := range []string{"```bash\none\ntwo", "… (+1 more lines)\n```", "</details>"} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q in %q", want, page)
		}
	}
	if strings.Contains(page, "three") {
		t.Fatalf("fenced command exceeded configured limit: %q", page)
	}
}

func TestCardRendererClosesLimitedCommandFenceBeforeFoldedResult(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	preferences.ToolOutputLines = 2
	page := application.RenderCardEventPages(preferences, []application.CardEvent{{
		Kind:       application.CardEventToolCall,
		Text:       "Bash",
		Body:       "```bash\none\ntwo\nthree\n```",
		HasResult:  true,
		ResultBody: "result",
	}}, fixedCardOptions(3500)).Latest.RichMarkdown
	if !strings.Contains(page, "```bash\none\ntwo\n… (+5 more lines)\n```") {
		t.Fatalf("limited folded event left a broken fence: %q", page)
	}
}

func TestCardRendererReturnsAddressableEmptyPage(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	if err := preferences.SetCardEventVisibility(domain.CardEventThinking, false); err != nil {
		t.Fatal(err)
	}
	result := application.RenderCardEventPages(
		preferences, []application.CardEvent{{Kind: application.CardEventThinking, Body: "hidden"}},
		fixedCardOptions(3500),
	)
	if len(result.Pages) != 1 || result.Latest.Number != 1 || result.Latest.Count != 1 ||
		result.Latest.RichMarkdown != "" {
		t.Fatalf("empty result = %#v", result)
	}
}
