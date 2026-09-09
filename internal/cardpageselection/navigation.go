package cardpageselection

import "fmt"

// Reflow follows the tail or finds the pinned anchor in the current pages.
// An aged-out anchor falls back to the first surviving page without following.
func Reflow(view View, anchors [][]string) (View, error) {
	if view.Page < 1 || view.Pages < 1 || view.Page > view.Pages {
		return View{}, fmt.Errorf("page view must contain a current positive page within its positive total")
	}
	if err := validateAnchors(anchors); err != nil {
		return View{}, err
	}
	if view.FollowLatest {
		return viewAt(anchors, len(anchors), true), nil
	}
	if view.Anchor != "" {
		for i, page := range anchors {
			for _, anchor := range page {
				if anchor == view.Anchor {
					resolved := viewAt(anchors, i+1, false)
					resolved.Anchor = view.Anchor
					return resolved, nil
				}
			}
		}
		return viewAt(anchors, 1, false), nil
	}
	if view.Page <= len(anchors) {
		return viewAt(anchors, view.Page, false), nil
	}
	return viewAt(anchors, 1, false), nil
}

// Navigate accepts page_previous, page_next and page_latest. Previous and next
// wrap; landing on the current last page enables follow mode.
func Navigate(view View, action string, anchors [][]string) (View, error) {
	resolved, err := Reflow(view, anchors)
	if err != nil {
		return View{}, err
	}
	if action == "page_latest" {
		return viewAt(anchors, len(anchors), true), nil
	}
	target := resolved.Page
	switch action {
	case "page_previous":
		target--
	case "page_next":
		target++
	default:
		return View{}, fmt.Errorf("action %q is not a page navigation action", action)
	}
	if target < 1 {
		target = len(anchors)
	} else if target > len(anchors) {
		target = 1
	}
	return viewAt(anchors, target, target == len(anchors)), nil
}

func viewAt(anchors [][]string, page int, follow bool) View {
	return View{Page: page, Pages: len(anchors), Anchor: anchors[page-1][0], FollowLatest: follow}
}

func validateAnchors(anchors [][]string) error {
	if len(anchors) == 0 {
		return fmt.Errorf("paginated content must contain at least one page")
	}
	seen := make(map[string]struct{})
	for i, page := range anchors {
		if len(page) == 0 {
			return fmt.Errorf("content page %d must contain at least one anchor", i+1)
		}
		for _, anchor := range page {
			if anchor == "" {
				return fmt.Errorf("content page %d contains an empty anchor", i+1)
			}
			if _, exists := seen[anchor]; exists {
				return fmt.Errorf("paginated content anchors must be globally unique")
			}
			seen[anchor] = struct{}{}
		}
	}
	return nil
}
