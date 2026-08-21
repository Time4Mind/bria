package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/terminalimage"
)

func (h *Handler) editPaneScreen(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	message telegrambot.Message,
	generation uint64,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	h.cardEditMu.Lock()
	defer h.cardEditMu.Unlock()
	if !h.currentPaneGeneration(actor.UserID, generation) ||
		!h.screenMatchesRememberedPage(actor.UserID, ref, screen) ||
		!h.visibleSessionMatches(actor, ref) {
		return message, nil
	}
	card, ok, err := h.service.TelegramResponseCard(actor)
	if err != nil || !ok || card.Session != ref {
		return message, nil
	}
	edited, err := h.editCardTransportLocked(ctx, message, screen)
	if err == nil {
		if screen.Pane != nil {
			h.rememberPaneFileID(ref, screen.Pane.Hash, edited.RichMediaFileID)
		}
		h.rememberResolvedCardPage(actor.UserID, ref, screen)
	}
	return edited, err
}

func (h *Handler) sessionIsActive(
	actor application.Principal,
	ref domain.SessionRef,
) bool {
	active, err := h.service.ActiveSession(actor)
	return err == nil && active.Ref() == ref
}

func (h *Handler) sessionNeedsPaneRefresh(
	actor application.Principal,
	ref domain.SessionRef,
) bool {
	session, err := h.service.Session(actor, ref)
	if err != nil || !h.sessionIsActive(actor, ref) {
		return false
	}
	switch session.RuntimePhase {
	case domain.RuntimeStarting, domain.RuntimeRunning, domain.RuntimeWaitingInput,
		domain.RuntimeStopping:
		return true
	default:
		return false
	}
}

func (h *Handler) finishPaneRefresh(userID domain.UserID, generation uint64) {
	h.paneMu.Lock()
	defer h.paneMu.Unlock()
	if h.paneWorkers[userID] == generation {
		delete(h.paneWorkers, userID)
		delete(h.paneWorkerRefs, userID)
		delete(h.paneCancels, userID)
	}
}

func shouldAttachPane(preferences domain.UserPreferences, phase domain.RuntimePhase) bool {
	switch preferences.EffectiveTerminalSnapshots() {
	case domain.TerminalSnapshotAlways:
		return true
	case domain.TerminalSnapshotWorking:
		return phase == domain.RuntimeStarting || phase == domain.RuntimeRunning ||
			phase == domain.RuntimeStopping || phase == domain.RuntimeWaitingInput ||
			phase == domain.RuntimeDegraded
	default:
		return false
	}
}

func (h *Handler) attachPane(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	message telegrambot.Message,
	generation uint64,
	attempt int,
	screen *telegramui.Screen,
) {
	timing := paneAttachTiming{outcome: "capture_error"}
	defer func() {
		if timing.total() >= 25*time.Millisecond {
			processlog.Detailf(
				"bria telegram: pane_timing mode=refresh ref=%q total_ms=%d "+
					"capture_ms=%d render_ms=%d outcome=%s",
				ref.Key(), timing.total().Milliseconds(), timing.capture.Milliseconds(),
				timing.render.Milliseconds(), timing.outcome,
			)
		}
	}()
	captureCtx, cancel := context.WithTimeout(ctx, paneBackgroundWait)
	defer cancel()
	startedAt := time.Now()
	pane, err := h.capturePaneCoalesced(captureCtx, actor, ref,
		fmt.Sprintf("pane-%d-%d-%d", message.MessageID, generation, attempt))
	timing.capture = time.Since(startedAt)
	if err != nil {
		if cached, ok := h.cachedPaneImage(ref, 0); ok {
			cached.AnchorOffset = paneAnchorOffset(*screen)
			screen.Pane = &cached
			timing.outcome = "stale_cache"
		}
		return
	}
	timing.outcome = "render_error"
	startedAt = time.Now()
	rendered, err := terminalimage.Render(string(pane), terminalimage.Options{})
	timing.render = time.Since(startedAt)
	if err != nil {
		return
	}
	timing.outcome = "attached"
	screen.Pane = &telegramui.PaneImage{
		PNG: rendered.PNG, Hash: rendered.Hash, AnchorOffset: paneAnchorOffset(*screen),
	}
	remembered := h.rememberPaneImage(ref, *screen.Pane)
	screen.Pane = &remembered
	if remembered.FileID != "" {
		timing.outcome = "verified_cache"
	}
}

func (h *Handler) attachImmediatePane(
	ctx context.Context,
	actor application.Principal,
	session domain.Session,
	screen *telegramui.Screen,
) paneAttachTiming {
	timing := paneAttachTiming{outcome: "capture_error"}
	startedAt := time.Now()
	ref := session.Ref()
	cached, hasCached := h.cachedPaneImage(ref, 0)
	// An idle or archived terminal cannot be changing on behalf of Bria, so its
	// latest image is safe to reuse without a capture. A working session must be
	// captured and rendered first: only an equal image hash may reuse file_id.
	if hasCached && paneCacheMaySkipCapture(session) {
		timing.cache = time.Since(startedAt)
		timing.outcome = "stable_cache"
		cached.AnchorOffset = paneAnchorOffset(*screen)
		screen.Pane = &cached
		return timing
	}
	timing.cache = time.Since(startedAt)
	captureCtx, cancel := context.WithTimeout(ctx, paneForegroundWait)
	defer cancel()
	startedAt = time.Now()
	pane, err := h.capturePaneCoalesced(captureCtx, actor, ref,
		fmt.Sprintf("pane-open-%d", time.Now().UnixNano()))
	timing.capture = time.Since(startedAt)
	if err != nil {
		if hasCached {
			cached.AnchorOffset = paneAnchorOffset(*screen)
			screen.Pane = &cached
			timing.outcome = "stale_cache"
		}
		return timing
	}
	timing.outcome = "render_error"
	startedAt = time.Now()
	rendered, err := terminalimage.Render(string(pane), terminalimage.Options{})
	timing.render = time.Since(startedAt)
	if err != nil {
		return timing
	}
	image := telegramui.PaneImage{
		PNG: rendered.PNG, Hash: rendered.Hash, AnchorOffset: paneAnchorOffset(*screen),
	}
	sameImage := hasCached && cached.Hash == image.Hash
	image = h.rememberPaneImage(ref, image)
	screen.Pane = &image
	switch {
	case sameImage && image.FileID != "":
		timing.outcome = "verified_cache"
	case sameImage:
		timing.outcome = "verified_png"
	case hasCached:
		timing.outcome = "changed"
	default:
		timing.outcome = "attached"
	}
	return timing
}

func (h *Handler) capturePaneCoalesced(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	operationID string,
) ([]byte, error) {
	key := ref.Key()
	h.paneMu.Lock()
	if h.paneCaptures == nil {
		h.paneCaptures = make(map[string]*paneCaptureFlight)
	}
	flight := h.paneCaptures[key]
	if flight == nil {
		flight = &paneCaptureFlight{done: make(chan struct{})}
		h.paneCaptures[key] = flight
		go h.runPaneCapture(actor, ref, operationID, flight)
	}
	h.paneMu.Unlock()
	select {
	case <-ctx.Done():
		processlog.Detailf(
			"bria telegram: pane_capture_phase ref=%q outcome=foreground_timeout error=%v",
			ref.Key(), ctx.Err(),
		)
		return nil, ctx.Err()
	case <-flight.done:
		return append([]byte(nil), flight.pane...), flight.err
	}
}

func (h *Handler) runPaneCapture(
	actor application.Principal,
	ref domain.SessionRef,
	operationID string,
	flight *paneCaptureFlight,
) {
	captureCtx, cancel := context.WithTimeout(context.Background(), paneCaptureExecution)
	defer cancel()
	startedAt := time.Now()
	flight.pane, flight.err = h.controls.CapturePane(captureCtx, actor, operationID, ref)
	outcome := "ok"
	if flight.err != nil {
		switch {
		case errors.Is(flight.err, context.DeadlineExceeded):
			outcome = "tmux_timeout"
		case errors.Is(flight.err, runtimehost.ErrStaleRuntime):
			outcome = "stale_generation"
		case errors.Is(flight.err, runtimehost.ErrRuntimeUnavailable):
			outcome = "target_missing"
		default:
			outcome = "error"
		}
	}
	processlog.Detailf(
		"bria telegram: pane_capture_phase ref=%q execution_ms=%d outcome=%s error=%v",
		ref.Key(), time.Since(startedAt).Milliseconds(), outcome, flight.err,
	)
	h.paneMu.Lock()
	if h.paneCaptures[keyForPaneCapture(ref)] == flight {
		delete(h.paneCaptures, keyForPaneCapture(ref))
	}
	close(flight.done)
	h.paneMu.Unlock()
}

func keyForPaneCapture(ref domain.SessionRef) string { return ref.Key() }

func (h *Handler) attachCachedPaneFileID(
	ref domain.SessionRef,
	screen *telegramui.Screen,
) bool {
	cached, ok := h.cachedPaneImage(ref, 0)
	if !ok || cached.FileID == "" {
		return false
	}
	cached.PNG = nil
	cached.AnchorOffset = paneAnchorOffset(*screen)
	screen.Pane = &cached
	return true
}

func reuseMessagePaneFileID(
	message telegrambot.Message,
	screen *telegramui.Screen,
) bool {
	if message.RichMediaFileID == "" {
		return false
	}
	screen.Pane = &telegramui.PaneImage{
		FileID: message.RichMediaFileID,
		Hash:   message.PaneHash, AnchorOffset: paneAnchorOffset(*screen),
	}
	return true
}

func paneCacheMaySkipCapture(session domain.Session) bool {
	return session.State == domain.SessionArchived || session.RuntimePhase == domain.RuntimeIdle
}

func paneAnchorOffset(screen telegramui.Screen) int {
	if screen.PaneAnchorOffset > 0 && screen.PaneAnchorOffset <= len(screen.Text) &&
		utf8.ValidString(screen.Text[:screen.PaneAnchorOffset]) {
		return screen.PaneAnchorOffset
	}
	return len(screen.Text)
}
