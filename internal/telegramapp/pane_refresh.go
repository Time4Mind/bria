package telegramapp

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
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
