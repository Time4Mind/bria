package telegramapp

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/terminalimage"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	paneInitialDelay         = 500 * time.Millisecond
	paneWorkingRefreshDelay  = 2500 * time.Millisecond
	paneResponseRefreshDelay = 500 * time.Millisecond
	paneRefreshLimit         = 1500
	paneCaptureLimit         = time.Second
	typingRefreshDelay       = 4 * time.Second
)

type paneAttachTiming struct {
	cache   time.Duration
	capture time.Duration
	render  time.Duration
	outcome string
}

func (timing paneAttachTiming) total() time.Duration {
	return timing.cache + timing.capture + timing.render
}

// schedulePaneRefresh never delays an update handler. A newer card generation
// supersedes the old finite worker, and the poller context owns cancellation.
func (h *Handler) schedulePaneRefresh(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	message telegrambot.Message,
) {
	if h.controls == nil || message.ChatID == 0 || message.MessageID == 0 {
		return
	}
	h.paneMu.Lock()
	h.paneGeneration[actor.UserID]++
	generation := h.paneGeneration[actor.UserID]
	h.paneWorkers[actor.UserID] = generation
	h.paneMu.Unlock()
	lastTyping := time.Time{}
	if session, err := h.service.Session(actor, ref); err == nil &&
		(session.RuntimePhase == domain.RuntimeStarting || session.RuntimePhase == domain.RuntimeRunning) {
		_ = h.messenger.SendTyping(ctx, message.ChatID)
		lastTyping = time.Now()
	}
	go h.runPaneRefresh(ctx, actor, ref, message, generation, lastTyping)
}

// ensurePaneRefresh restores the live-card poller after a leader or Bria
// process restart. tmux outlives the node process, while goroutines do not;
// without this guard a replicated running session can keep executing with a
// permanently frozen Telegram card.
func (h *Handler) ensurePaneRefresh(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	message telegrambot.Message,
) {
	h.paneMu.Lock()
	_, running := h.paneWorkers[actor.UserID]
	h.paneMu.Unlock()
	if !running {
		h.schedulePaneRefresh(ctx, actor, ref, message)
	}
}

func (h *Handler) runPaneRefresh(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	message telegrambot.Message,
	generation uint64,
	lastTyping time.Time,
) {
	defer h.finishPaneRefresh(actor.UserID, generation)
	delay := paneInitialDelay
	responseRefresh := false
	for attempt := 0; attempt < paneRefreshLimit; attempt++ {
		if !waitPaneRefresh(ctx, delay) || !h.canRefresh() ||
			!h.currentPaneGeneration(actor.UserID, generation) {
			return
		}
		if retryAfter, blocked := h.activity.editFloodWait(message.ChatID); blocked {
			// Keep this generation registered so the recovery watchdog does not
			// start a fresh worker every 1.2s. Final-answer reconciliation uses a
			// send and remains independent of the edit cooldown.
			delay = retryAfter
			continue
		}
		session, err := h.service.Session(actor, ref)
		if err != nil {
			return
		}
		if !h.sessionIsActive(actor, ref) {
			return
		}
		if (session.RuntimePhase == domain.RuntimeStarting ||
			session.RuntimePhase == domain.RuntimeRunning) &&
			time.Since(lastTyping) >= typingRefreshDelay {
			// Ephemeral transport feedback is best effort and cannot reject a
			// durably accepted prompt.
			_ = h.messenger.SendTyping(ctx, message.ChatID)
			lastTyping = time.Now()
		}
		if session.RuntimePhase == domain.RuntimeStarting {
			delay = paneWorkingRefreshDelay
			continue
		}
		if session.RuntimePhase == domain.RuntimeWaitingInput {
			page := h.rememberedCardPage(actor.UserID, ref)
			screen, renderErr := h.renderSessionCard(ctx, actor, ref, page)
			if renderErr == nil && h.sessionIsActive(actor, ref) &&
				h.currentPaneGeneration(actor.UserID, generation) {
				_, _ = h.editResponseCard(ctx, actor, message, screen)
			}
			return
		}
		// Runtime reconciliation may observe the final transcript and publish
		// idle before this live-card worker gets its next turn. Render once more
		// so that race cannot leave the Telegram card on a tool result or pane.
		if session.RuntimePhase == domain.RuntimeIdle {
			page := h.rememberedCardPage(actor.UserID, ref)
			snapshot, renderErr := h.renderSessionCardSnapshot(ctx, actor, ref, page)
			if renderErr == nil && h.sessionIsActive(actor, ref) &&
				h.currentPaneGeneration(actor.UserID, generation) {
				if finalAt, ok := finalTranscriptAt(snapshot.events); ok &&
					transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) {
					snapshot, renderErr = h.renderSessionCardSnapshot(
						ctx, actor, ref, application.CardPageLatestResponseStart,
					)
					if renderErr != nil {
						return
					}
					_, _ = h.repostFinalResponseCard(
						ctx, actor, message, ref, snapshot.screen,
					)
				} else {
					_, _ = h.editResponseCard(ctx, actor, message, snapshot.screen)
				}
			}
			return
		}
		if session.RuntimePhase != domain.RuntimeRunning {
			if session.RuntimePhase == domain.RuntimeDegraded {
				page := h.rememberedCardPage(actor.UserID, ref)
				screen, renderErr := h.renderSessionCard(ctx, actor, ref, page)
				if renderErr == nil && h.sessionIsActive(actor, ref) &&
					h.currentPaneGeneration(actor.UserID, generation) {
					_, _ = h.editResponseCard(ctx, actor, message, screen)
				}
			}
			return
		}
		page := h.rememberedCardPage(actor.UserID, ref)
		snapshot, err := h.renderSessionCardSnapshotWithoutPane(ctx, actor, ref, page)
		if err != nil {
			return
		}
		settled := h.settleFromTranscript(ctx, actor, session, snapshot.events)
		if settled {
			snapshot, err = h.renderSessionCardSnapshotWithoutPane(
				ctx, actor, ref, application.CardPageLatestResponseStart,
			)
			if err != nil {
				return
			}
		}
		if !h.sessionIsActive(actor, ref) ||
			!h.currentPaneGeneration(actor.UserID, generation) {
			return
		}
		preferences, preferencesErr := h.service.Preferences(actor)
		panePhase := session.RuntimePhase
		if settled {
			panePhase = domain.RuntimeIdle
			if final, ok := finalTranscriptEvent(snapshot.events); ok && final.Error {
				panePhase = domain.RuntimeDegraded
			}
		}
		if preferencesErr == nil && shouldAttachPane(preferences, panePhase) {
			h.attachPane(ctx, actor, ref, message, generation, attempt, &snapshot.screen)
		}
		if settled {
			message, err = h.repostFinalResponseCard(ctx, actor, message, ref, snapshot.screen)
		} else if !message.Rich && (snapshot.screen.RichMarkdown || snapshot.screen.Pane != nil) {
			message, err = h.editResponseCard(ctx, actor, message, snapshot.screen)
		} else {
			// Keep high-frequency live-pane edits local to this worker. Replicating
			// every screenshot hash would add avoidable Raft traffic; durable
			// transport metadata is needed only at a settled interaction boundary.
			message, err = h.editPaneScreen(
				ctx, actor, ref, message, generation, snapshot.screen,
			)
		}
		if err != nil {
			return
		}
		if settled {
			return
		}
		nextDelay := nextPaneRefreshDelay(session, snapshot.events)
		responseStarted := nextDelay == paneResponseRefreshDelay
		if responseStarted && !responseRefresh {
			fmt.Printf(
				"bria telegram: pane_refresh_mode ref=%q mode=response interval_ms=%d\n",
				ref.Key(), paneResponseRefreshDelay.Milliseconds(),
			)
		}
		responseRefresh = responseStarted
		delay = nextDelay
	}
}

const currentTurnUserTimestampSkew = 5 * time.Second

func nextPaneRefreshDelay(session domain.Session, events []transcript.Event) time.Duration {
	if currentTurnEndsWithAssistantResponse(session, events) {
		return paneResponseRefreshDelay
	}
	return paneWorkingRefreshDelay
}

// currentTurnEndsWithAssistantResponse enables the fast cadence only while the
// provider transcript tail is model output for the current input. A later tool
// or thinking event returns the worker to the conservative cadence, preventing
// long tool-heavy turns from flooding Telegram after an early commentary row.
func currentTurnEndsWithAssistantResponse(
	session domain.Session,
	events []transcript.Event,
) bool {
	operation := session.LastOperation
	if operation == nil || operation.Action != domain.ActionSendInput {
		return false
	}
	userIndex := -1
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind != transcript.EventUserText {
			continue
		}
		userAt, err := time.Parse(time.RFC3339Nano, events[index].Timestamp)
		if err != nil || userAt.Before(operation.At.Add(-currentTurnUserTimestampSkew)) {
			return false
		}
		userIndex = index
		break
	}
	if userIndex < 0 {
		return false
	}
	responseTail := false
	for index := userIndex + 1; index < len(events); index++ {
		switch events[index].Kind {
		case transcript.EventAssistantText, transcript.EventAssistantFinal:
			responseTail = true
		case transcript.EventThinking, transcript.EventToolCall, transcript.EventToolResult:
			responseTail = false
		}
	}
	return responseTail
}

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
		!h.screenMatchesRememberedPage(actor.UserID, ref, screen) {
		return message, nil
	}
	card, ok, err := h.service.TelegramResponseCard(actor)
	if err != nil || !ok || card.Session != ref {
		return message, nil
	}
	edited, err := h.messenger.EditScreen(ctx, message, screen)
	if _, limited := telegrambot.RemoteFloodWait(err); limited {
		h.cardMutationMu.Lock()
		edited, err = h.replaceFloodLimitedResponseCardLocked(
			ctx, actor, message, ref, screen, err,
		)
		h.cardMutationMu.Unlock()
	}
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
			fmt.Printf(
				"bria telegram: pane_timing mode=refresh ref=%q total_ms=%d "+
					"capture_ms=%d render_ms=%d outcome=%s\n",
				ref.Key(), timing.total().Milliseconds(), timing.capture.Milliseconds(),
				timing.render.Milliseconds(), timing.outcome,
			)
		}
	}()
	captureCtx, cancel := context.WithTimeout(ctx, paneCaptureLimit)
	defer cancel()
	startedAt := time.Now()
	pane, err := h.controls.CapturePane(
		captureCtx, actor,
		fmt.Sprintf("pane-%d-%d-%d", message.MessageID, generation, attempt), ref,
	)
	timing.capture = time.Since(startedAt)
	if err != nil {
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
	captureCtx, cancel := context.WithTimeout(ctx, paneCaptureLimit)
	defer cancel()
	startedAt = time.Now()
	pane, err := h.controls.CapturePane(captureCtx, actor,
		fmt.Sprintf("pane-open-%d", time.Now().UnixNano()), ref)
	timing.capture = time.Since(startedAt)
	if err != nil {
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

func (h *Handler) settleFromTranscript(
	ctx context.Context,
	actor application.Principal,
	session domain.Session,
	events []transcript.Event,
) bool {
	if session.RuntimePhase != domain.RuntimeRunning {
		return false
	}
	turn, ok := transcript.LatestCompletedTurn(events)
	if !ok {
		return false
	}
	final := turn.Final
	finalAt := turn.FinalAt
	if turn.HasUser && turn.UserAt.Before(session.LastEventAt) {
		return false
	}
	if !transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) {
		return false
	}
	settleCtx := application.WithOperationScope(
		ctx, fmt.Sprintf("transcript-final-%s-%d", session.Ref().Key(), finalAt.UnixNano()),
	)
	phase := domain.RuntimeIdle
	var result *domain.SessionOperationResult
	if final.Error {
		phase = domain.RuntimeDegraded
		result = &domain.SessionOperationResult{
			OperationID: fmt.Sprintf("transcript-error-%d", finalAt.UnixNano()),
			Action:      domain.ActionSendInput, Status: domain.OperationFailed,
			Detail: boundedOperationDetail(final.Text),
		}
	}
	if h.service.PublishSessionRuntime(settleCtx, session, phase, result) != nil {
		return false
	}
	if !final.Error {
		go h.deliverFinalFiles(ctx, actor, session.Ref(), events)
	}
	return true
}

func boundedOperationDetail(value string) string {
	value = strings.TrimSpace(value)
	for len(value) > 512 {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

const deliveredInputTimestampSkew = 2 * time.Minute
const deliveredInputSettleGrace = 2 * time.Second

func transcriptFinalBelongsToCurrentTurn(
	session domain.Session,
	finalAt time.Time,
	now time.Time,
) bool {
	if !finalAt.Before(session.LastEventAt) {
		return true
	}
	operation := session.LastOperation
	if operation != nil && operation.Action == domain.ActionSendInput &&
		operation.Status == domain.OperationQueued &&
		session.RuntimePhase == domain.RuntimeIdle {
		// Transcript settlement and Telegram delivery are separate durable
		// steps. If the process exits after publishing idle but before reposting
		// the card, LastEventAt contains the settlement time while LastOperation
		// still contains the queued prompt. The operation timestamp is the
		// durable turn boundary: a final after it belongs to that prompt, while
		// a previous turn's final does not.
		return !finalAt.Before(operation.At) &&
			session.LastEventAt.Sub(finalAt) <= deliveredInputTimestampSkew
	}
	// Older builds stamped LastEventAt again when the local node acknowledged
	// an already-delivered prompt. A fast backend can finish before that
	// acknowledgement is replicated. Accept only that narrow legacy skew; all
	// other finals older than the current turn remain stale.
	return operation != nil && operation.Action == domain.ActionSendInput &&
		operation.Status == domain.OperationSucceeded &&
		session.LastEventAt.Sub(finalAt) <= deliveredInputTimestampSkew &&
		!now.Before(operation.At.Add(deliveredInputSettleGrace))
}

func (h *Handler) currentPaneGeneration(userID domain.UserID, generation uint64) bool {
	h.paneMu.Lock()
	defer h.paneMu.Unlock()
	return h.paneGeneration[userID] == generation
}

func (h *Handler) cancelPaneRefresh(userID domain.UserID) {
	h.paneMu.Lock()
	h.paneGeneration[userID]++
	delete(h.paneWorkers, userID)
	h.paneMu.Unlock()
}

func waitPaneRefresh(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
