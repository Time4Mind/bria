package telegramui

import "fmt"

// ReflowPageView resolves a remembered position against newly paginated
// content. Follow mode tracks the current tail; a pinned view tracks its stable
// anchor and falls back to the oldest surviving page when that anchor aged out.
func ReflowPageView(view PageView, pages []ContentPage) (PageView, error) {
	if err := validatePageView(view); err != nil {
		return PageView{}, err
	}
	if err := validateContentPages(pages); err != nil {
		return PageView{}, err
	}
	if view.FollowLatest {
		return pageViewAt(pages, len(pages), true), nil
	}
	if view.Anchor != "" {
		for index, page := range pages {
			if containsAnchor(page.Anchors, view.Anchor) {
				resolved := pageViewAt(pages, index+1, false)
				resolved.Anchor = view.Anchor
				return resolved, nil
			}
		}
		return pageViewAt(pages, 1, false), nil
	}
	if view.Page <= len(pages) {
		return pageViewAt(pages, view.Page, false), nil
	}
	return pageViewAt(pages, 1, false), nil
}

func validateContentPages(pages []ContentPage) error {
	if len(pages) == 0 {
		return fmt.Errorf("paginated content must contain at least one page")
	}
	seen := make(map[string]struct{})
	for pageIndex, page := range pages {
		if len(page.Anchors) == 0 {
			return fmt.Errorf("content page %d must contain at least one anchor", pageIndex+1)
		}
		for _, anchor := range page.Anchors {
			if anchor == "" {
				return fmt.Errorf("content page %d contains an empty anchor", pageIndex+1)
			}
			if _, exists := seen[anchor]; exists {
				return fmt.Errorf("paginated content anchors must be globally unique")
			}
			seen[anchor] = struct{}{}
		}
	}
	return nil
}

// NavigatePage applies one of the three page actions against current content.
// Previous and next wrap. Landing on the latest page enables follow mode;
// ActionPageLatest always resolves the current tail, even from stale state.
func NavigatePage(view PageView, action Action, pages []ContentPage) (PageView, error) {
	resolved, err := ReflowPageView(view, pages)
	if err != nil {
		return PageView{}, err
	}
	if action == ActionPageLatest {
		return pageViewAt(pages, len(pages), true), nil
	}

	target := 0
	switch action {
	case ActionPagePrevious:
		target = wrappedPage(resolved.Page-1, len(pages))
	case ActionPageNext:
		target = wrappedPage(resolved.Page+1, len(pages))
	default:
		return PageView{}, fmt.Errorf("action %q is not a page navigation action", action)
	}
	return pageViewAt(pages, target, target == len(pages)), nil
}

func pageViewAt(pages []ContentPage, page int, follow bool) PageView {
	return PageView{
		Page:         page,
		Pages:        len(pages),
		Anchor:       firstAnchor(pages[page-1]),
		FollowLatest: follow,
	}
}

func firstAnchor(page ContentPage) string {
	if len(page.Anchors) == 0 {
		return ""
	}
	return page.Anchors[0]
}

func containsAnchor(anchors []string, want string) bool {
	for _, anchor := range anchors {
		if anchor == want {
			return true
		}
	}
	return false
}
