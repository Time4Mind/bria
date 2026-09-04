package telegramcontroller

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPaginateSemanticHistoryKeepsOnlyConfiguredLatestWindow(t *testing.T) {
	items := make([]string, 40)
	for index := range items {
		items[index] = strings.Repeat(string(rune('a'+index%26)), 3200)
	}
	pages := paginateSemanticHistory(items, 32)
	if len(pages) != 32 {
		t.Fatalf("page count = %d, want configured maximum 32", len(pages))
	}
	if pages[0].Content != items[8] || pages[len(pages)-1].Content != items[39] {
		t.Fatal("bounded projection did not retain the latest 32 pages")
	}
}

func TestPaginateSemanticHistorySplitsOversizedUTF8ItemsAtValidBoundaries(t *testing.T) {
	item := strings.Repeat("я", 2000)
	pages := paginateSemanticHistory([]string{item}, 64)
	if len(pages) != 2 {
		t.Fatalf("page count = %d, want 2", len(pages))
	}
	joined := ""
	for index, page := range pages {
		if len(page.Content) > 3200 || !utf8.ValidString(page.Content) {
			t.Fatalf("page %d is invalid: bytes=%d", index, len(page.Content))
		}
		joined += page.Content
	}
	if joined != item {
		t.Fatal("UTF-8 split changed semantic history content")
	}
}
