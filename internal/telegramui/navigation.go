package telegramui

import "bria/internal/cardpageselection"

// ReflowPageView resolves a remembered position against newly paginated
// content. Follow mode tracks the current tail; a pinned view tracks its stable
// anchor and falls back to the oldest surviving page when that anchor aged out.
func ReflowPageView(view PageView, pages []ContentPage) (PageView, error) {
	resolved, err := cardpageselection.Reflow(cardpageselection.View(view), pageAnchors(pages))
	return PageView(resolved), err
}

func validateContentPages(pages []ContentPage) error {
	_, err := cardpageselection.Reflow(cardpageselection.View{Page: 1, Pages: 1}, pageAnchors(pages))
	return err
}

// NavigatePage applies one of the three page actions against current content.
// Previous and next wrap. Landing on the latest page enables follow mode;
// ActionPageLatest always resolves the current tail, even from stale state.
func NavigatePage(view PageView, action Action, pages []ContentPage) (PageView, error) {
	resolved, err := cardpageselection.Navigate(cardpageselection.View(view), string(action), pageAnchors(pages))
	return PageView(resolved), err
}

func pageAnchors(pages []ContentPage) [][]string {
	anchors := make([][]string, len(pages))
	for index, page := range pages {
		anchors[index] = page.Anchors
	}
	return anchors
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
