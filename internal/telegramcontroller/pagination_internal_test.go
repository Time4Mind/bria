package telegramcontroller

import (
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/cardtranscript"
)

func TestPaginateSemanticHistoryKeepsOnlyConfiguredLatestWindow(t *testing.T) {
	items := make([]cardtranscript.Block, 40)
	for index := range items {
		items[index].Text = strings.Repeat(string(rune('a'+index%26)), 3000)
	}
	pages := cardtranscript.Paginate(items, 32)
	if len(pages) != 32 {
		t.Fatalf("page count = %d, want configured maximum 32", len(pages))
	}
	if pages[0].Content != items[8].Text || pages[len(pages)-1].Content != items[39].Text {
		t.Fatal("bounded projection did not retain the latest 32 pages")
	}
}

func TestPaginateSemanticHistorySplitsOversizedUTF8ItemsAtValidBoundaries(t *testing.T) {
	item := strings.Repeat("я", 2000)
	pages := cardtranscript.Paginate([]cardtranscript.Block{{Text: item}}, 64)
	if len(pages) != 2 {
		t.Fatalf("page count = %d, want 2", len(pages))
	}
	joined := ""
	for index, page := range pages {
		if len(page.Content) > 3000 || !utf8.ValidString(page.Content) {
			t.Fatalf("page %d is invalid: bytes=%d", index, len(page.Content))
		}
		joined += page.Content
	}
	if joined != item {
		t.Fatal("UTF-8 split changed semantic history content")
	}
}
