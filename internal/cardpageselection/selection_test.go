package cardpageselection_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"bria/internal/cardpageselection"
	"bria/internal/cardtranscript"
	"bria/internal/domain"
)

func TestResolveKeepsReadingIntentAndNavigatesCurrentPages(t *testing.T) {
	pages := []cardtranscript.Page{{Content: "B", Anchors: []string{"b"}}, {Content: "C", Anchors: []string{"c"}}, {Content: "D", Anchors: []string{"d"}}}
	cases := []struct {
		name, action string
		view, want   cardpageselection.View
	}{
		{"first", "", cardpageselection.View{}, cardpageselection.View{Page: 3, Pages: 3, Anchor: "d", FollowLatest: true}},
		{"follow", "", cardpageselection.View{Page: 2, Pages: 2, Anchor: "b", FollowLatest: true}, cardpageselection.View{Page: 3, Pages: 3, Anchor: "d", FollowLatest: true}},
		{"pin", "", cardpageselection.View{Page: 2, Pages: 3, Anchor: "b"}, cardpageselection.View{Page: 1, Pages: 3, Anchor: "b"}},
		{"notice", "status notice", cardpageselection.View{Page: 2, Pages: 3, Anchor: "b"}, cardpageselection.View{Page: 1, Pages: 3, Anchor: "b"}},
		{"previous_tail", "pg:prev", cardpageselection.View{Page: 2, Pages: 2, Anchor: "b", FollowLatest: true}, cardpageselection.View{Page: 2, Pages: 3, Anchor: "c"}},
		{"next_tail", "pg:next", cardpageselection.View{Page: 2, Pages: 3, Anchor: "c"}, cardpageselection.View{Page: 3, Pages: 3, Anchor: "d", FollowLatest: true}},
		{"absolute_previous", "pg:target:1", cardpageselection.View{Page: 3, Pages: 3, Anchor: "d", FollowLatest: true}, cardpageselection.View{Page: 1, Pages: 3, Anchor: "b"}},
		{"expired_anchor", "", cardpageselection.View{Page: 1, Pages: 3, Anchor: "a"}, cardpageselection.View{Page: 1, Pages: 3, Anchor: "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cardpageselection.Resolve(tc.view, pages, tc.action)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolve=%#v %v want=%#v", got, err, tc.want)
			}
		})
	}
}

type failingReader struct{ err error }

func (r failingReader) LoadCardPage(context.Context, domain.SessionID) (int, int, string, bool, bool, error) {
	return 0, 0, "", false, false, r.err
}

func TestSelectNeverHidesDurableReadFailure(t *testing.T) {
	fault := errors.New("synthetic durable view read failure")
	_, err := cardpageselection.Select(context.Background(), failingReader{fault}, "session", cardpageselection.View{}, []cardtranscript.Page{{Content: "a", Anchors: []string{"a"}}}, "")
	if !errors.Is(err, fault) {
		t.Fatalf("durable read error became fallback view: %v", err)
	}
}
