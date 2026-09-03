package telegramui_test

import (
	"reflect"
	"testing"

	"bria/internal/telegramui"
)

func TestReflowPageViewPreservesAnchorAndFollowIntent(t *testing.T) {
	pages := []telegramui.ContentPage{
		{Content: "new oldest", Anchors: []string{"new-oldest"}},
		{Content: "before", Anchors: []string{"before"}},
		{Content: "kept", Anchors: []string{"kept"}},
		{Content: "latest", Anchors: []string{"latest"}},
	}

	for _, test := range []struct {
		name string
		view telegramui.PageView
		want telegramui.PageView
	}{
		{
			name: "surviving pinned anchor moves with content",
			view: telegramui.PageView{Page: 2, Pages: 3, Anchor: "kept"},
			want: telegramui.PageView{Page: 3, Pages: 4, Anchor: "kept"},
		},
		{
			name: "aged out anchor chooses oldest surviving page",
			view: telegramui.PageView{Page: 2, Pages: 3, Anchor: "aged-out"},
			want: telegramui.PageView{Page: 1, Pages: 4, Anchor: "new-oldest"},
		},
		{
			name: "follow chooses current latest despite stale total",
			view: telegramui.PageView{Page: 2, Pages: 2, Anchor: "old-latest", FollowLatest: true},
			want: telegramui.PageView{Page: 4, Pages: 4, Anchor: "latest", FollowLatest: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := telegramui.ReflowPageView(test.view, pages)
			if err != nil {
				t.Fatalf("ReflowPageView() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ReflowPageView() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNavigatePageWrapsAndLatestRestoresFollowAgainstCurrentPages(t *testing.T) {
	pages := []telegramui.ContentPage{
		{Content: "first", Anchors: []string{"first"}},
		{Content: "middle", Anchors: []string{"middle"}},
		{Content: "last", Anchors: []string{"last"}},
	}

	previous, err := telegramui.NavigatePage(
		telegramui.PageView{Page: 1, Pages: 3, Anchor: "first"},
		telegramui.ActionPagePrevious,
		pages,
	)
	if err != nil {
		t.Fatalf("NavigatePage(previous) error = %v", err)
	}
	if want := (telegramui.PageView{Page: 3, Pages: 3, Anchor: "last", FollowLatest: true}); previous != want {
		t.Fatalf("previous from first = %#v, want %#v", previous, want)
	}

	next, err := telegramui.NavigatePage(previous, telegramui.ActionPageNext, pages)
	if err != nil {
		t.Fatalf("NavigatePage(next) error = %v", err)
	}
	if want := (telegramui.PageView{Page: 1, Pages: 3, Anchor: "first"}); next != want {
		t.Fatalf("next from latest = %#v, want %#v", next, want)
	}

	latestPages := append(append([]telegramui.ContentPage(nil), pages...),
		telegramui.ContentPage{Content: "new last", Anchors: []string{"new-last"}})
	latest, err := telegramui.NavigatePage(
		telegramui.PageView{Page: 2, Pages: 2, Anchor: "middle"},
		telegramui.ActionPageLatest,
		latestPages,
	)
	if err != nil {
		t.Fatalf("NavigatePage(latest) error = %v", err)
	}
	if want := (telegramui.PageView{Page: 4, Pages: 4, Anchor: "new-last", FollowLatest: true}); !reflect.DeepEqual(latest, want) {
		t.Fatalf("latest from stale view = %#v, want %#v", latest, want)
	}
}

func TestReflowPageViewRejectsAmbiguousOrMissingPageAnchors(t *testing.T) {
	tests := []struct {
		name  string
		pages []telegramui.ContentPage
	}{
		{
			name: "page without anchor",
			pages: []telegramui.ContentPage{
				{Content: "unanchored"},
			},
		},
		{
			name: "empty anchor",
			pages: []telegramui.ContentPage{
				{Content: "ambiguous", Anchors: []string{""}},
			},
		},
		{
			name: "anchor repeated across pages",
			pages: []telegramui.ContentPage{
				{Content: "first", Anchors: []string{"same"}},
				{Content: "second", Anchors: []string{"same"}},
			},
		},
		{
			name: "anchor repeated within page",
			pages: []telegramui.ContentPage{
				{Content: "first", Anchors: []string{"same", "same"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := telegramui.ReflowPageView(
				telegramui.PageView{Page: 1, Pages: len(test.pages)},
				test.pages,
			)
			if err == nil {
				t.Fatal("ReflowPageView() error = nil, want invalid page anchor rejection")
			}
		})
	}
}
