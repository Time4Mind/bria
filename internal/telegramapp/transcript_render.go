package telegramapp

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func (h *Handler) renderSessionCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (telegramui.Screen, error) {
	return h.renderSessionCardForSelection(ctx, actor, ref, page, false)
}

func (h *Handler) renderSelectedSessionCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (telegramui.Screen, error) {
	return h.renderSessionCardForSelection(ctx, actor, ref, page, true)
}

func (h *Handler) renderSessionCardForSelection(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
	acknowledgeFinal bool,
) (telegramui.Screen, error) {
	if recovery, ok := h.controls.(interface {
		EnsureName(application.Principal, domain.SessionRef) bool
	}); ok {
		recovery.EnsureName(actor, ref)
	}
	if screen, ok, err := h.renderInteractiveSessionCard(ctx, actor, ref); ok || err != nil {
		return screen, err
	}
	var snapshot sessionCardSnapshot
	var err error
	if acknowledgeFinal {
		// Session selection is latency-sensitive. Transcript projection is local
		// and cheap; node transcript reads and terminal PNG rendering are not.
		// Prefer the snapshot that produced a recent card and let the live worker
		// reconcile it after the callback has completed.
		var cacheEligible bool
		cacheEligible, err = h.sessionSelectionMayUseCache(actor, ref)
		var cached bool
		if err == nil && cacheEligible {
			snapshot, cached, err = h.renderCachedSessionCardSnapshot(actor, ref, page)
		}
		if err == nil && (!cacheEligible || !cached) {
			snapshot, err = h.renderSessionCardSnapshotWithoutPane(ctx, actor, ref, page)
		}
		if err == nil {
			h.attachCachedPaneFileID(ref, &snapshot.screen)
		}
	} else {
		snapshot, err = h.renderSessionCardSnapshot(ctx, actor, ref, page)
	}
	if err == nil && acknowledgeFinal && snapshot.screen.Checkpoint != nil {
		if finalAt, final := finalTranscriptAt(snapshot.events); final {
			// Explicitly selecting a completed session renders its existing card,
			// even when the user left that session on a historical page. Treat that
			// selection as delivery of the already-settled final; otherwise switching
			// away discards the current-card watermark and reconciliation reposts a
			// duplicate carrier as soon as the user switches back.
			snapshot.screen.Checkpoint.RenderedFinalAt = finalAt
		}
	}
	return snapshot.screen, err
}

// sessionSelectionMayUseCache keeps the callback fast only while the provider
// is actively changing the transcript and the live worker is responsible for
// reconciliation. Idle/completed cards are read synchronously so explicit
// selection preserves the established immediate-freshness contract. Pending
// voice input also forces a live read so the placeholder can be retired as
// soon as its transcript event appears.
func (h *Handler) sessionSelectionMayUseCache(
	actor application.Principal,
	ref domain.SessionRef,
) (bool, error) {
	session, err := h.service.Session(actor, ref)
	if err != nil {
		return false, err
	}
	if h.hasPendingVoice(actor, ref) {
		return false, nil
	}
	switch session.RuntimePhase {
	case domain.RuntimeRunning, domain.RuntimeWaitingInput, domain.RuntimeStopping:
		return true, nil
	default:
		return false, nil
	}
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

// renderCachedSessionCard keeps explicit pagination out of the node I/O path.
// The cache is exactly the transcript snapshot that produced a recent card;
// a live worker refreshes it asynchronously after the user's page edit.
func (h *Handler) renderCachedSessionCard(
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (telegramui.Screen, bool, error) {
	snapshot, ok, err := h.renderCachedSessionCardSnapshot(actor, ref, page)
	return snapshot.screen, ok, err
}

func (h *Handler) renderCachedSessionCardSnapshot(
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (sessionCardSnapshot, bool, error) {
	session, err := h.service.Session(actor, ref)
	if err != nil {
		return sessionCardSnapshot{}, false, err
	}
	events, ok := h.cachedCardTranscript(ref)
	if !ok {
		return sessionCardSnapshot{}, false, nil
	}
	renderedEvents := h.withPendingVoiceRows(actor, ref, session, events)
	screen, err := h.projector.SessionCardViewWithContext(
		actor, ref, cardEvents(renderedEvents), page,
		h.rememberedCardAnchor(actor.UserID, ref, page), h.cardContext(ref),
	)
	if err == nil {
		if finalAt, final := finalTranscriptAt(events); final && screen.Checkpoint != nil &&
			screenShowsLatestCardPage(screen) {
			screen.Checkpoint.RenderedFinalAt = finalAt
		}
	}
	return sessionCardSnapshot{screen: screen, events: events}, true, err
}

func (h *Handler) renderSessionCardSnapshot(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (sessionCardSnapshot, error) {
	return h.renderSessionCardSnapshotWithPane(ctx, actor, ref, page, true)
}

// renderSessionCardSnapshotWithoutPane is used by the live worker immediately
// before attachPane. Keeping that capture in one place avoids rendering the
// same changing terminal twice in a single refresh iteration.
func (h *Handler) renderSessionCardSnapshotWithoutPane(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
) (sessionCardSnapshot, error) {
	return h.renderSessionCardSnapshotWithPane(ctx, actor, ref, page, false)
}

func (h *Handler) renderSessionCardSnapshotWithPane(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	page int,
	attachPane bool,
) (sessionCardSnapshot, error) {
	if h.controls == nil {
		screen, err := h.projector.SessionCard(actor, ref)
		return sessionCardSnapshot{screen: screen}, err
	}
	timing := sessionCardTiming{
		startedAt: time.Now(), transcriptSource: "none", outcome: "ok",
		pane: paneAttachTiming{outcome: "skipped"},
	}
	defer func() { timing.log(ref, page) }()
	phaseStarted := time.Now()
	session, sessionErr := h.service.Session(actor, ref)
	timing.session = time.Since(phaseStarted)
	if sessionErr != nil {
		timing.outcome = "session_error"
		return sessionCardSnapshot{}, sessionErr
	}
	phaseStarted = time.Now()
	events, err := h.controls.Transcript(ctx, actor, ref)
	timing.transcript = time.Since(phaseStarted)
	if err != nil {
		// A transient node-control failure must not erase a previously rendered
		// transcript by replacing the live card with its header-only projection.
		// Reuse the bounded in-memory copy when possible; otherwise leave the
		// existing Telegram card untouched until the transcript is reachable.
		if cached, ok := h.cachedCardTranscript(ref); ok {
			timing.transcriptSource = "cache"
			events = cached
		} else if session.ProviderSessionID == "" ||
			(session.IsLive() && errors.Is(err, transcript.ErrTranscriptNotFound)) {
			// Claude assigns the provider session ID before its first prompt, but
			// does not create the JSONL transcript until that prompt is accepted.
			// A freshly provisioned live session therefore has a legitimate empty
			// transcript and must still receive its usable Telegram card.
			timing.transcriptSource = "empty"
			phaseStarted = time.Now()
			renderedEvents := h.withPendingVoiceRows(actor, ref, session, nil)
			timing.pending = time.Since(phaseStarted)
			phaseStarted = time.Now()
			screen, projectErr := h.projector.SessionCardViewWithContext(
				actor, ref, cardEvents(renderedEvents), page,
				h.rememberedCardAnchor(actor.UserID, ref, page), h.cardContext(ref),
			)
			timing.projection = time.Since(phaseStarted)
			timing.events = len(renderedEvents)
			if projectErr != nil {
				timing.outcome = "projection_error"
			}
			return sessionCardSnapshot{screen: screen}, projectErr
		} else {
			timing.transcriptSource = "error"
			timing.outcome = "transcript_error"
			return sessionCardSnapshot{}, err
		}
	} else {
		timing.transcriptSource = "live"
	}
	phaseStarted = time.Now()
	events = h.rememberCardTranscript(
		ref, session.Revision, session.ProviderSessionID, events,
	)
	timing.cache = time.Since(phaseStarted)
	phaseStarted = time.Now()
	renderedEvents := h.withPendingVoiceRows(actor, ref, session, events)
	timing.pending = time.Since(phaseStarted)
	phaseStarted = time.Now()
	screen, err := h.projector.SessionCardViewWithContext(
		actor, ref, cardEvents(renderedEvents), page,
		h.rememberedCardAnchor(actor.UserID, ref, page), h.cardContext(ref),
	)
	timing.projection = time.Since(phaseStarted)
	timing.events = len(renderedEvents)
	if err != nil {
		timing.outcome = "projection_error"
	}
	if err == nil {
		if finalAt, final := finalTranscriptAt(events); final && screen.Checkpoint != nil &&
			(screenShowsLatestCardPage(screen) || page == application.CardPageLatestResponseStart) {
			screen.Checkpoint.RenderedFinalAt = finalAt
		}
		phaseStarted = time.Now()
		preferences, preferencesErr := h.service.Preferences(actor)
		timing.preferences = time.Since(phaseStarted)
		if attachPane && preferencesErr == nil &&
			preferences.EffectiveTerminalSnapshots() == domain.TerminalSnapshotAlways {
			timing.pane = h.attachImmediatePane(ctx, actor, session, &screen)
		}
	}
	return sessionCardSnapshot{screen: screen, events: events}, err
}
