package telegramapp

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

const maxCallbackCardPages = 512

type sessionCardSnapshot struct {
	screen telegramui.Screen
	events []transcript.Event
}

type sessionCardTiming struct {
	startedAt        time.Time
	session          time.Duration
	transcript       time.Duration
	cache            time.Duration
	pending          time.Duration
	projection       time.Duration
	preferences      time.Duration
	pane             paneAttachTiming
	transcriptSource string
	outcome          string
	events           int
}

func (timing sessionCardTiming) log(ref domain.SessionRef, page int) {
	total := time.Since(timing.startedAt)
	processlog.Detailf(
		"bria telegram: card_timing ref=%q page=%d total_ms=%d session_ms=%d "+
			"transcript_ms=%d cache_ms=%d pending_ms=%d projection_ms=%d "+
			"preferences_ms=%d pane_ms=%d pane_cache_ms=%d pane_capture_ms=%d pane_render_ms=%d "+
			"events=%d transcript_source=%s pane_outcome=%s outcome=%s",
		ref.Key(), page, total.Milliseconds(), timing.session.Milliseconds(),
		timing.transcript.Milliseconds(), timing.cache.Milliseconds(),
		timing.pending.Milliseconds(), timing.projection.Milliseconds(),
		timing.preferences.Milliseconds(), timing.pane.total().Milliseconds(),
		timing.pane.cache.Milliseconds(), timing.pane.capture.Milliseconds(),
		timing.pane.render.Milliseconds(),
		timing.events, timing.transcriptSource, timing.pane.outcome, timing.outcome,
	)
}

type sessionPageKey struct {
	userID    domain.UserID
	nodeID    domain.NodeID
	sessionID domain.SessionID
}

type cardPageState struct {
	page   int
	pages  int
	follow bool
}

func (h *Handler) openSessionPage(
	ctx context.Context,
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (telegramui.Screen, domain.SessionRef, error) {
	refs, err := h.service.CallbackSessionCandidates(actor)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	candidates := make([]callbacktoken.SessionPage, 0, len(refs)*maxCallbackCardPages)
	for _, ref := range refs {
		for page := 1; page <= maxCallbackCardPages; page++ {
			candidates = append(candidates, callbacktoken.SessionPage{Session: ref, Page: page})
		}
	}
	target, err := h.tokens.ResolvePage(actor.UserID, action, token, candidates)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	page := target.Page
	if action == telegramui.ActionPageLatest {
		// The token carries the page count from the rendered keyboard and may be
		// stale by the time it is tapped. Zero resolves against the current
		// transcript and explicitly restores follow mode.
		page = 0
	} else if action == telegramui.ActionPagePrevious || action == telegramui.ActionPageNext {
		key := pageKey(actor.UserID, target.Session)
		h.pageMu.Lock()
		state, ok := h.sessionPages[key]
		h.pageMu.Unlock()
		if ok && state.page > 0 && state.pages > 0 {
			page = state.page - 1
			if action == telegramui.ActionPageNext {
				page = state.page + 1
			}
			page = wrappedCardPage(page, state.pages)
		}
	}
	screen, err := h.renderSessionCard(ctx, actor, target.Session, page)
	return screen, target.Session, err
}

func (h *Handler) rememberCardPage(
	userID domain.UserID,
	screen telegramui.Screen,
) {
	if screen.Name != telegramui.ScreenSessionCard || screen.Checkpoint == nil {
		return
	}
	ref := domain.SessionRef{
		NodeID:    domain.NodeID(screen.Checkpoint.NodeID),
		SessionID: domain.SessionID(screen.Checkpoint.SessionID),
	}
	if ref.Validate() != nil {
		return
	}
	h.rememberResolvedCardPage(userID, ref, screen)
}

func (h *Handler) rememberResolvedCardPage(
	userID domain.UserID,
	ref domain.SessionRef,
	screen telegramui.Screen,
) {
	if len(screen.Grid) == 0 || len(screen.Grid[0]) < 2 {
		return
	}
	page, pages, ok := parseCardPageLabel(screen.Grid[0][1].Label)
	if !ok {
		return
	}
	key := pageKey(userID, ref)
	h.pageMu.Lock()
	h.sessionPages[key] = cardPageState{
		page: page, pages: pages, follow: page == pages,
	}
	h.pageMu.Unlock()
}

func (h *Handler) rememberedCardPage(
	userID domain.UserID,
	ref domain.SessionRef,
) int {
	key := pageKey(userID, ref)
	h.pageMu.Lock()
	defer h.pageMu.Unlock()
	state, ok := h.sessionPages[key]
	if !ok || state.page < 1 {
		return 0
	}
	if state.follow {
		return 0
	}
	return state.page
}

func pageKey(userID domain.UserID, ref domain.SessionRef) sessionPageKey {
	return sessionPageKey{userID: userID, nodeID: ref.NodeID, sessionID: ref.SessionID}
}

func (h *Handler) screenMatchesRememberedPage(
	userID domain.UserID,
	ref domain.SessionRef,
	screen telegramui.Screen,
) bool {
	want := h.rememberedCardPage(userID, ref)
	if want == 0 {
		return true
	}
	if len(screen.Grid) == 0 || len(screen.Grid[0]) < 2 {
		return false
	}
	page, _, ok := parseCardPageLabel(screen.Grid[0][1].Label)
	return ok && page == want
}

func parseCardPageLabel(label string) (int, int, bool) {
	left, right, ok := strings.Cut(strings.TrimSpace(label), "/")
	if !ok {
		return 0, 0, false
	}
	page, pageErr := strconv.Atoi(left)
	pages, pagesErr := strconv.Atoi(right)
	return page, pages, pageErr == nil && pagesErr == nil && page > 0 && page <= pages
}

func wrappedCardPage(page, pages int) int {
	if pages <= 1 {
		return 1
	}
	if page < 1 {
		return pages
	}
	if page > pages {
		return 1
	}
	return page
}
