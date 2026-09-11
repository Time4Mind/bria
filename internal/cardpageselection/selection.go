// Package cardpageselection resolves durable reading intent against current pages.
package cardpageselection

import (
	"context"
	"sync"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
)

// View is transport-neutral reading intent; page numbers are one-based.
type View struct {
	Page, Pages  int
	Anchor       string
	FollowLatest bool
}

// Memory keeps the controller's transport-independent page-plan cache.
type Memory struct {
	mu    sync.Mutex
	views map[domain.SessionID]View
}

func NewMemory() *Memory {
	return &Memory{views: make(map[domain.SessionID]View)}
}

func (memory *Memory) Load(id domain.SessionID) View {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return memory.views[id]
}

func (memory *Memory) Store(id domain.SessionID, view View) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.views[id] = view
}

func (memory *Memory) ResetFollow(id domain.SessionID) {
	memory.Store(id, View{FollowLatest: true})
}

// Persist writes a resolved page plan when the supplied store supports it.
func Persist(ctx context.Context, store any, id domain.SessionID, view View) error {
	writer, ok := store.(interface {
		SetCardPage(context.Context, domain.SessionID, int, int, string, bool) error
	})
	if !ok {
		return nil
	}
	return writer.SetCardPage(ctx, id, view.Page, view.Pages, view.Anchor, view.FollowLatest)
}

// Load reads the optional primitive store boundary without changing state.
func Load(ctx context.Context, store any, id domain.SessionID) (View, bool, error) {
	reader, ok := store.(interface {
		LoadCardPage(context.Context, domain.SessionID) (int, int, string, bool, bool, error)
	})
	if !ok {
		return View{}, false, nil
	}
	page, pages, anchor, follow, found, err := reader.LoadCardPage(ctx, id)
	return View{Page: page, Pages: pages, Anchor: anchor, FollowLatest: follow}, found, err
}

// Select prefers durable view commits, including a newly delivered final card,
// over a controller's pre-delivery cache. Stores without a reader use fallback.
func Select(ctx context.Context, store any, id domain.SessionID, fallback View, pages []cardtranscript.Page, action string) (View, error) {
	view, found, err := Load(ctx, store, id)
	if err != nil {
		return View{}, err
	}
	if !found {
		view = fallback
	}
	return Resolve(view, pages, action)
}

// Resolve follows the current tail or preserves a historical anchor. Only
// explicit navigation changes follow intent; status text is not navigation.
func Resolve(view View, pages []cardtranscript.Page, action string) (View, error) {
	if view.Page == 0 {
		view = View{Page: 1, Pages: len(pages), FollowLatest: true}
	}
	anchors := make([][]string, len(pages))
	for i, page := range pages {
		anchors[i] = page.Anchors
	}
	switch action {
	case "card:prev", "pg:prev", "page_previous":
		return Navigate(view, "page_previous", anchors)
	case "card:next", "pg:next", "page_next":
		return Navigate(view, "page_next", anchors)
	case "card:latest", "pg:jump", "page_latest":
		return Navigate(view, "page_latest", anchors)
	default:
		return Reflow(view, anchors)
	}
}
