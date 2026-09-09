package cardpageselection_test

import (
	"testing"

	"bria/internal/cardpageselection"
)

func TestReflowPreservesInteriorAnchorAndLegacyPosition(t *testing.T) {
	anchors := [][]string{{"a", "b"}, {"c"}}
	for _, tc := range []struct {
		name       string
		view, want cardpageselection.View
	}{
		{"interior", cardpageselection.View{Page: 2, Pages: 3, Anchor: "b"}, cardpageselection.View{Page: 1, Pages: 2, Anchor: "b"}},
		{"legacy", cardpageselection.View{Page: 2, Pages: 3}, cardpageselection.View{Page: 2, Pages: 2, Anchor: "c"}},
		{"legacy_expired", cardpageselection.View{Page: 3, Pages: 3}, cardpageselection.View{Page: 1, Pages: 2, Anchor: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cardpageselection.Reflow(tc.view, anchors)
			if err != nil || got != tc.want {
				t.Fatalf("reflow=%+v %v want=%+v", got, err, tc.want)
			}
		})
	}
}

func TestNavigateCanonicalActionsWrapAndTrackTail(t *testing.T) {
	anchors := [][]string{{"a"}, {"b"}, {"c"}}
	for _, tc := range []struct {
		action     string
		view, want cardpageselection.View
	}{
		{"page_previous", cardpageselection.View{Page: 1, Pages: 3, Anchor: "a"}, cardpageselection.View{Page: 3, Pages: 3, Anchor: "c", FollowLatest: true}},
		{"page_next", cardpageselection.View{Page: 3, Pages: 3, FollowLatest: true}, cardpageselection.View{Page: 1, Pages: 3, Anchor: "a"}},
		{"page_latest", cardpageselection.View{Page: 1, Pages: 1, Anchor: "a"}, cardpageselection.View{Page: 3, Pages: 3, Anchor: "c", FollowLatest: true}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			got, err := cardpageselection.Navigate(tc.view, tc.action, anchors)
			if err != nil || got != tc.want {
				t.Fatalf("navigate=%+v %v want=%+v", got, err, tc.want)
			}
		})
	}
}

func TestNeutralNavigationRejectsInvalidViewAnchorsAndAction(t *testing.T) {
	valid := cardpageselection.View{Page: 1, Pages: 1}
	for _, tc := range []struct {
		name    string
		view    cardpageselection.View
		anchors [][]string
	}{
		{"zero_view", cardpageselection.View{}, [][]string{{"a"}}},
		{"outside_total", cardpageselection.View{Page: 2, Pages: 1}, [][]string{{"a"}}},
		{"no_pages", valid, nil},
		{"no_anchors", valid, [][]string{nil}},
		{"empty_anchor", valid, [][]string{{""}}},
		{"duplicate", valid, [][]string{{"a"}, {"a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cardpageselection.Reflow(tc.view, tc.anchors); err == nil {
				t.Fatal("invalid page state accepted")
			}
			if _, err := cardpageselection.Navigate(tc.view, "page_next", tc.anchors); err == nil {
				t.Fatal("invalid navigation state accepted")
			}
		})
	}
	if _, err := cardpageselection.Navigate(valid, "status notice", [][]string{{"a"}}); err == nil {
		t.Fatal("unknown navigation action accepted")
	}
}
