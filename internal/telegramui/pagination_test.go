package telegramui_test

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/telegramui"
)

func TestPaginateContentIsBoundedDeterministicAndLossless(t *testing.T) {
	blocks := []telegramui.ContentBlock{
		{Anchor: "request", Content: "alpha beta"},
		{Anchor: "answer", Content: " γδε", BreakBefore: true},
	}
	limits := telegramui.PageLimits{MaxRunes: 5, MaxBytes: 5}

	first, err := telegramui.PaginateContent(blocks, limits)
	if err != nil {
		t.Fatalf("PaginateContent() error = %v", err)
	}
	second, err := telegramui.PaginateContent(blocks, limits)
	if err != nil {
		t.Fatalf("second PaginateContent() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("pagination is not deterministic: first=%#v second=%#v", first, second)
	}

	var joined strings.Builder
	for index, page := range first.Pages {
		joined.WriteString(page.Content)
		if got := utf8.RuneCountInString(page.Content); got > limits.MaxRunes {
			t.Errorf("page %d has %d runes, limit %d", index+1, got, limits.MaxRunes)
		}
		if got := len(page.Content); got > limits.MaxBytes {
			t.Errorf("page %d has %d bytes, limit %d", index+1, got, limits.MaxBytes)
		}
	}
	if got, want := joined.String(), "alpha beta γδε"; got != want {
		t.Fatalf("joined pages = %q, want exact input %q", got, want)
	}
	if got, want := first.Pages, []telegramui.ContentPage{
		{Content: "alpha", Anchors: []string{"request"}},
		{Content: " beta", Anchors: []string{"request\x001"}},
		{Content: " γδ", Anchors: []string{"answer"}},
		{Content: "ε", Anchors: []string{"answer\x001"}},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pages = %#v, want %#v", got, want)
	}
}

func TestPaginateContentRequiresPositiveLimits(t *testing.T) {
	for _, limits := range []telegramui.PageLimits{
		{MaxRunes: 0, MaxBytes: 10},
		{MaxRunes: 10, MaxBytes: 0},
		{MaxRunes: -1, MaxBytes: 10},
		{MaxRunes: 10, MaxBytes: -1},
	} {
		if _, err := telegramui.PaginateContent(
			[]telegramui.ContentBlock{{Anchor: "one", Content: "content"}}, limits,
		); err == nil {
			t.Fatalf("PaginateContent(%#v) error = nil, want invalid limits", limits)
		}
	}
}

func TestPaginateContentKeepsAnOversizedBlocksPinnedChunkStable(t *testing.T) {
	pagination, err := telegramui.PaginateContent(
		[]telegramui.ContentBlock{{Anchor: "response", Content: "abcdefghij"}},
		telegramui.PageLimits{MaxRunes: 5, MaxBytes: 5},
	)
	if err != nil {
		t.Fatalf("PaginateContent() error = %v", err)
	}
	if len(pagination.Pages) != 2 || len(pagination.Pages[1].Anchors) != 1 {
		t.Fatalf("pages = %#v, want two anchored chunks", pagination.Pages)
	}
	secondAnchor := pagination.Pages[1].Anchors[0]
	if secondAnchor == pagination.Pages[0].Anchors[0] {
		t.Fatalf("split chunks share anchor %q", secondAnchor)
	}

	reflowedPages := append([]telegramui.ContentPage{{
		Content: "leading", Anchors: []string{"leading"},
	}}, pagination.Pages...)
	view, err := telegramui.ReflowPageView(
		telegramui.PageView{Page: 2, Pages: 2, Anchor: secondAnchor},
		reflowedPages,
	)
	if err != nil {
		t.Fatalf("ReflowPageView() error = %v", err)
	}
	if view.Page != 3 || view.Anchor != secondAnchor {
		t.Fatalf("reflowed split chunk = %#v, want page 3 anchor %q", view, secondAnchor)
	}
}

func TestPaginateContentRejectsInvalidUTF8WithoutPanicking(t *testing.T) {
	if _, err := telegramui.PaginateContent(
		[]telegramui.ContentBlock{{Anchor: "invalid", Content: string([]byte{0xff})}},
		telegramui.PageLimits{MaxRunes: 1, MaxBytes: 3},
	); err == nil {
		t.Fatal("PaginateContent() error = nil, want invalid UTF-8 rejection")
	}
}

func TestPaginateContentReturnsOnlyUsableAnchoredPages(t *testing.T) {
	for _, blocks := range [][]telegramui.ContentBlock{
		{{Content: "unanchored"}},
		{{Anchor: "empty", Content: ""}},
	} {
		if _, err := telegramui.PaginateContent(
			blocks,
			telegramui.PageLimits{MaxRunes: 10, MaxBytes: 10},
		); err == nil {
			t.Fatalf("PaginateContent(%#v) error = nil, want unusable page rejection", blocks)
		}
	}
}
