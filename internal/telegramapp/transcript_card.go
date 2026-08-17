package telegramapp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

const maxCallbackCardPages = 512

type sessionCardSnapshot struct {
	screen telegramui.Screen
	events []transcript.Event
}

type cardPageKey struct {
	userID    domain.UserID
	chatID    int64
	messageID int64
}

type cardPageState struct {
	ref    domain.SessionRef
	page   int
	pages  int
	follow bool
}

func (h *Handler) openSessionPage(
	ctx context.Context,
	actor application.Principal,
	origin telegrambot.Message,
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
		key := cardPageKey{userID: actor.UserID, chatID: origin.ChatID, messageID: origin.MessageID}
		h.pageMu.Lock()
		state, ok := h.cardPages[key]
		h.pageMu.Unlock()
		if ok && state.ref == target.Session && state.page > 0 && state.pages > 0 {
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
	message telegrambot.Message,
	screen telegramui.Screen,
) {
	if screen.Name != telegramui.ScreenSessionCard || message.ChatID == 0 || message.MessageID == 0 ||
		len(screen.Grid) == 0 || len(screen.Grid[0]) < 2 {
		return
	}
	page, pages, ok := parseCardPageLabel(screen.Grid[0][1].Label)
	if !ok {
		return
	}
	key := cardPageKey{userID: userID, chatID: message.ChatID, messageID: message.MessageID}
	h.pageMu.Lock()
	state := h.cardPages[key]
	state.page, state.pages = page, pages
	state.follow = page == pages
	h.cardPages[key] = state
	h.pageMu.Unlock()
}

func (h *Handler) rememberResolvedCardPage(
	userID domain.UserID,
	message telegrambot.Message,
	ref domain.SessionRef,
	screen telegramui.Screen,
) {
	h.rememberCardPage(userID, message, screen)
	key := cardPageKey{userID: userID, chatID: message.ChatID, messageID: message.MessageID}
	h.pageMu.Lock()
	state := h.cardPages[key]
	state.ref = ref
	h.cardPages[key] = state
	h.pageMu.Unlock()
}

func (h *Handler) rememberedCardPage(
	userID domain.UserID,
	message telegrambot.Message,
	ref domain.SessionRef,
) int {
	key := cardPageKey{userID: userID, chatID: message.ChatID, messageID: message.MessageID}
	h.pageMu.Lock()
	defer h.pageMu.Unlock()
	state, ok := h.cardPages[key]
	if !ok || state.ref != ref || state.page < 1 {
		return 0
	}
	if state.follow {
		return 0
	}
	return state.page
}

func (h *Handler) screenMatchesRememberedPage(
	userID domain.UserID,
	message telegrambot.Message,
	ref domain.SessionRef,
	screen telegramui.Screen,
) bool {
	want := h.rememberedCardPage(userID, message, ref)
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

func (h *Handler) renderSessionCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (telegramui.Screen, error) {
	if recovery, ok := h.controls.(interface {
		EnsureName(application.Principal, domain.SessionRef) bool
	}); ok {
		recovery.EnsureName(actor, ref)
	}
	if screen, ok, err := h.renderInteractiveSessionCard(ctx, actor, ref); ok || err != nil {
		return screen, err
	}
	return h.renderRegularSessionCard(ctx, actor, ref, page)
}

func (h *Handler) renderRegularSessionCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (telegramui.Screen, error) {
	snapshot, err := h.renderSessionCardSnapshot(ctx, actor, ref, page)
	return snapshot.screen, err
}

func (h *Handler) renderSessionCardSnapshot(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (sessionCardSnapshot, error) {
	if h.controls == nil {
		screen, err := h.projector.SessionCard(actor, ref)
		return sessionCardSnapshot{screen: screen}, err
	}
	session, sessionErr := h.service.Session(actor, ref)
	if sessionErr != nil {
		return sessionCardSnapshot{}, sessionErr
	}
	events, err := h.controls.Transcript(ctx, actor, ref)
	if err != nil {
		// A transient node-control failure must not erase a previously rendered
		// transcript by replacing the live card with its header-only projection.
		// Reuse the bounded in-memory copy when possible; otherwise leave the
		// existing Telegram card untouched until the transcript is reachable.
		if cached, ok := h.cachedCardTranscript(ref); ok {
			events = cached
		} else if session.ProviderSessionID == "" ||
			(session.IsLive() && errors.Is(err, transcript.ErrTranscriptNotFound)) {
			// Claude assigns the provider session ID before its first prompt, but
			// does not create the JSONL transcript until that prompt is accepted.
			// A freshly provisioned live session therefore has a legitimate empty
			// transcript and must still receive its usable Telegram card.
			renderedEvents := h.withPendingVoiceRows(actor, ref, session, nil)
			screen, projectErr := h.projector.SessionCardPageWithContext(
				actor, ref, cardEvents(renderedEvents), page, h.cardContext(ref),
			)
			return sessionCardSnapshot{screen: screen}, projectErr
		} else {
			return sessionCardSnapshot{}, err
		}
	}
	h.rememberCardTranscript(ref, session.Revision, events)
	renderedEvents := h.withPendingVoiceRows(actor, ref, session, events)
	screen, err := h.projector.SessionCardPageWithContext(
		actor, ref, cardEvents(renderedEvents), page, h.cardContext(ref),
	)
	if err == nil {
		if finalAt, final := finalTranscriptAt(events); final && screen.Checkpoint != nil &&
			(screenShowsLatestCardPage(screen) || page == application.CardPageLatestResponseStart) {
			screen.Checkpoint.RenderedFinalAt = finalAt
		}
		preferences, preferencesErr := h.service.Preferences(actor)
		if preferencesErr == nil &&
			preferences.EffectiveTerminalSnapshots() == domain.TerminalSnapshotAlways {
			h.attachImmediatePane(ctx, actor, ref, &screen)
		}
	}
	return sessionCardSnapshot{screen: screen, events: events}, err
}

func cardEvents(events []transcript.Event) []application.CardEvent {
	result := make([]application.CardEvent, 0, len(events))
	for _, event := range events {
		kind, pageBreak := cardEventKind(event.Kind)
		if kind == "" {
			continue
		}
		startedAt, _ := time.Parse(time.RFC3339Nano, event.Timestamp)
		text := event.Text
		if event.Kind == transcript.EventAssistantText ||
			event.Kind == transcript.EventAssistantFinal {
			text = stripTrailingAssistantMetadata(text)
		}
		if event.Head != "" {
			text = event.Head
		}
		result = append(result, application.CardEvent{
			Kind: kind, Text: text, Body: event.Body,
			ToolUseID: event.ToolUseID, ToolName: event.ToolName,
			StartedAt: startedAt, IsError: event.Error, PageBreak: pageBreak,
		})
	}
	return result
}

const (
	assistantMetadataOpen  = "<oai-mem-citation>"
	assistantMetadataClose = "</oai-mem-citation>"
)

// stripTrailingAssistantMetadata removes transport-only metadata appended by
// Codex after the user-facing answer. It intentionally requires a complete
// trailing block and runs only for assistant events, so user text, code samples,
// ordinary HTML, and incomplete tag-shaped content remain visible verbatim.
func stripTrailingAssistantMetadata(text string) string {
	trimmed := strings.TrimSpace(text)
	for strings.HasSuffix(trimmed, assistantMetadataClose) {
		start := strings.LastIndex(trimmed, assistantMetadataOpen)
		if start < 0 || (start > 0 && trimmed[start-1] != '\n') {
			break
		}
		trimmed = strings.TrimSpace(trimmed[:start])
	}
	return trimmed
}

func cardEventKind(kind transcript.EventKind) (application.CardEventKind, bool) {
	switch kind {
	case transcript.EventUserText:
		return application.CardEventUserText, false
	case transcript.EventAssistantText:
		return application.CardEventAssistantText, false
	case transcript.EventAssistantFinal:
		return application.CardEventAssistantText, true
	case transcript.EventThinking:
		return application.CardEventThinking, false
	case transcript.EventToolCall:
		return application.CardEventToolCall, false
	case transcript.EventToolResult:
		return application.CardEventToolResult, false
	default:
		return "", false
	}
}

func finalTranscriptAt(events []transcript.Event) (time.Time, bool) {
	final, ok := finalTranscriptEvent(events)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, final.Timestamp)
	return at, err == nil
}

func finalTranscriptEvent(events []transcript.Event) (transcript.Event, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind != transcript.EventAssistantFinal {
			if events[index].Kind == transcript.EventUserText ||
				events[index].Kind == transcript.EventAssistantText ||
				events[index].Kind == transcript.EventThinking ||
				events[index].Kind == transcript.EventToolCall {
				return transcript.Event{}, false
			}
			continue
		}
		return events[index], true
	}
	return transcript.Event{}, false
}
