package application_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestCardRendererRepeatsMarkdownTableHeaderAcrossPages(t *testing.T) {
	const header = "| Аспект | Преимущество | Недостаток | Итог |"
	const separator = "|---|---|---|---|"
	rows := make([]string, 18)
	for index := range rows {
		rows[index] = fmt.Sprintf(
			"| row-%02d | Преимущество %s | Недостаток %s | **Вывод %02d** |",
			index+1, strings.Repeat("длинное ", 5), strings.Repeat("подробное ", 4), index+1,
		)
	}
	answer := "Краткое введение перед сравнением.\n\n" + header + "\n" + separator + "\n" +
		strings.Join(rows, "\n") + "\n\nИтог после таблицы."

	result := application.RenderCardEventPages(
		domain.DefaultUserPreferences(),
		[]application.CardEvent{{Kind: application.CardEventAssistantText, Text: answer}},
		application.CardRenderOptions{MaxPageRunes: 800, MaxPageBytes: 1200},
	)
	if len(result.Pages) < 3 {
		t.Fatalf("expected a multi-page table, got %d pages", len(result.Pages))
	}

	combined := strings.Builder{}
	tablePages := 0
	for index, page := range result.Pages {
		text := page.RichMarkdown
		if utf8.RuneCountInString(text) > 800 || len(text) > 1200 {
			t.Fatalf("page %d exceeds bounds: %d runes, %d bytes", index+1,
				utf8.RuneCountInString(text), len(text))
		}
		if strings.Contains(text, "| row-") {
			tablePages++
			headerAt := strings.Index(text, header)
			separatorAt := strings.Index(text, separator)
			firstRowAt := strings.Index(text, "| row-")
			if headerAt < 0 || separatorAt < headerAt || firstRowAt < separatorAt {
				t.Errorf("page %d has a broken continued table:\n%s", index+1, text)
			}
		}
		combined.WriteString(text)
		combined.WriteByte('\n')
	}
	if tablePages < 2 {
		t.Fatalf("table was not continued across pages: %#v", result.Pages)
	}
	allPages := combined.String()
	for index := range rows {
		marker := fmt.Sprintf("row-%02d", index+1)
		if count := strings.Count(allPages, marker); count != 1 {
			t.Errorf("%s occurs %d times, want once", marker, count)
		}
	}
	if !strings.Contains(result.Pages[0].RichMarkdown, "Краткое введение") ||
		!strings.Contains(result.Latest.RichMarkdown, "Итог после таблицы") {
		t.Fatalf("surrounding prose was lost: first=%q latest=%q",
			result.Pages[0].RichMarkdown, result.Latest.RichMarkdown)
	}
}

func TestCardRendererDoesNotTreatPipeTextAsMarkdownTable(t *testing.T) {
	answer := "Обычный текст | с разделителем\n| но без строки-разделителя |\nПродолжение."
	result := application.RenderCardEventPages(
		domain.DefaultUserPreferences(),
		[]application.CardEvent{{Kind: application.CardEventAssistantText, Text: answer}},
		application.CardRenderOptions{MaxPageRunes: 500, MaxPageBytes: 800},
	)
	if len(result.Pages) != 1 || result.Latest.RichMarkdown != answer {
		t.Fatalf("non-table text changed: %#v", result.Pages)
	}
}
