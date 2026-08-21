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
const tracedSessionCardTiming = 25 * time.Millisecond

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

func (timing sessionCardTiming) log(ctx context.Context, ref domain.SessionRef, page int) {
	total := time.Since(timing.startedAt)
	if !shouldLogSessionCardTiming(timing, total) {
		return
	}
	restoreTag, restoreTagged := restoreTimingFromContext(ctx)
	restoreFields := ""
	if restoreTagged {
		restoreFields = " restore_stage=" + restoreTag.stage +
			" restore_generation=" + strconv.FormatUint(restoreTag.generation, 10)
	}
	failureClass := processlog.FailureClassForOutcome(timing.outcome)
	if failureClass == processlog.FailureNone && strings.Contains(timing.pane.outcome, "error") {
		failureClass = processlog.FailureClassForOutcome(timing.pane.outcome)
	}
	processlog.Failuref(processlog.Detail, failureClass,
		"bria telegram: card_timing ref=%q page=%d total_ms=%d session_ms=%d "+
			"transcript_ms=%d cache_ms=%d pending_ms=%d projection_ms=%d "+
			"preferences_ms=%d pane_ms=%d pane_cache_ms=%d pane_capture_ms=%d pane_render_ms=%d "+
			"events=%d transcript_source=%s pane_outcome=%s outcome=%s%s",
		ref.Key(), page, total.Milliseconds(), timing.session.Milliseconds(),
		timing.transcript.Milliseconds(), timing.cache.Milliseconds(),
		timing.pending.Milliseconds(), timing.projection.Milliseconds(),
		timing.preferences.Milliseconds(), timing.pane.total().Milliseconds(),
		timing.pane.cache.Milliseconds(), timing.pane.capture.Milliseconds(),
		timing.pane.render.Milliseconds(),
		timing.events, timing.transcriptSource, timing.pane.outcome, timing.outcome,
		restoreFields,
	)
}

func shouldLogSessionCardTiming(timing sessionCardTiming, total time.Duration) bool {
	return total >= tracedSessionCardTiming || timing.outcome != "ok" ||
		strings.Contains(timing.pane.outcome, "error")
}

type sessionPageKey struct {
	userID    domain.UserID
	nodeID    domain.NodeID
	sessionID domain.SessionID
}

type cardPageState struct {
	page   int
	pages  int
	anchor string
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
	var screen telegramui.Screen
	cached := false
	if action == telegramui.ActionPagePrevious || action == telegramui.ActionPageNext {
		screen, cached, err = h.renderCachedSessionCard(actor, target.Session, page)
	}
	if err == nil && !cached {
		screen, err = h.renderSessionCard(ctx, actor, target.Session, page)
	}
	if err == nil {
		h.rememberResolvedCardPageWithFollow(
			actor.UserID, target.Session, screen, action == telegramui.ActionPageLatest,
		)
	}
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
	state, ok := h.cardPageState(userID, ref)
	if !ok {
		state.follow = true
	}
	h.rememberResolvedCardPageWithFollow(userID, ref, screen, state.follow)
}

func (h *Handler) rememberResolvedCardPageWithFollow(
	userID domain.UserID,
	ref domain.SessionRef,
	screen telegramui.Screen,
	follow bool,
) {
	if len(screen.Grid) == 0 || len(screen.Grid[0]) < 2 {
		return
	}
	page, pages, ok := parseCardPageLabel(screen.Grid[0][1].Label)
	if !ok {
		return
	}
	anchor := ""
	if screen.Checkpoint != nil {
		anchor = screen.Checkpoint.PageAnchor
	}
	key := pageKey(userID, ref)
	h.pageMu.Lock()
	h.sessionPages[key] = cardPageState{
		page: page, pages: pages, anchor: anchor, follow: follow,
	}
	h.pageMu.Unlock()
}

func (h *Handler) cardPageState(
	userID domain.UserID,
	ref domain.SessionRef,
) (cardPageState, bool) {
	key := pageKey(userID, ref)
	h.pageMu.Lock()
	state, ok := h.sessionPages[key]
	h.pageMu.Unlock()
	if ok {
		return state, true
	}
	if h.service != nil {
		view, exists, err := h.service.TelegramSessionView(
			application.Principal{UserID: userID}, ref,
		)
		if err == nil && exists {
			state = cardPageState{
				page: view.Page, pages: view.Pages, anchor: view.Anchor, follow: view.Follow,
			}
			ok = true
		}
	}
	if !ok && h.service != nil {
		card, exists, err := h.service.TelegramResponseCard(application.Principal{UserID: userID})
		if err == nil && exists && card.Session == ref {
			if decoded, _, marked := decodeSessionPagePaneHash(card.PaneHash); marked {
				state = decoded
				ok = true
			}
		}
	}
	if ok {
		h.pageMu.Lock()
		h.sessionPages[key] = state
		h.pageMu.Unlock()
	}
	return state, ok
}

func (h *Handler) rememberedCardPage(
	userID domain.UserID,
	ref domain.SessionRef,
) int {
	state, ok := h.cardPageState(userID, ref)
	if !ok || state.page < 1 {
		return 0
	}
	if state.follow {
		return 0
	}
	return state.page
}

func (h *Handler) rememberedCardAnchor(
	userID domain.UserID,
	ref domain.SessionRef,
	requestedPage int,
) string {
	if requestedPage < 1 {
		return ""
	}
	state, ok := h.cardPageState(userID, ref)
	if !ok || state.follow || state.page != requestedPage {
		return ""
	}
	return state.anchor
}

// restoreFollowForInput converts a numerical last-page position into explicit
// follow intent at the moment the user submits a new prompt. Merely landing on
// the last page during a background reflow must not do this; input is the
// deliberate boundary that makes the latest page live again.
func (h *Handler) restoreFollowForInput(userID domain.UserID, ref domain.SessionRef) {
	state, ok := h.cardPageState(userID, ref)
	if !ok || state.follow || state.page < 1 || state.page != state.pages {
		return
	}
	state.follow = true
	state.anchor = ""
	h.pageMu.Lock()
	h.sessionPages[pageKey(userID, ref)] = state
	h.pageMu.Unlock()
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
