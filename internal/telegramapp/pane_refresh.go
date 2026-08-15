package telegramapp

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/terminalimage"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	paneInitialDelay   = 500 * time.Millisecond
	paneRefreshDelay   = 1200 * time.Millisecond
	paneRefreshLimit   = 1500
	paneCaptureLimit   = time.Second
	typingRefreshDelay = 4 * time.Second
)

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
	for attempt := 0; attempt < paneRefreshLimit; attempt++ {
		if !waitPaneRefresh(ctx, delay) || !h.canRefresh() ||
			!h.currentPaneGeneration(actor.UserID, generation) {
			return
		}
		session, err := h.service.Session(actor, ref)
		if err != nil {
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
			delay = paneRefreshDelay
			continue
		}
		if session.RuntimePhase == domain.RuntimeWaitingInput {
			screen, renderErr := h.renderSessionCard(ctx, actor, ref, 0)
			if renderErr == nil {
				_, _ = h.editResponseCard(ctx, actor, message, screen)
			}
			return
		}
		// Runtime reconciliation may observe the final transcript and publish
		// idle before this live-card worker gets its next turn. Render once more
		// so that race cannot leave the Telegram card on a tool result or pane.
		if session.RuntimePhase == domain.RuntimeIdle {
			screen, renderErr := h.renderSessionCard(ctx, actor, ref, 0)
			if renderErr == nil {
				_, _ = h.editResponseCard(ctx, actor, message, screen)
			}
			return
		}
		if session.RuntimePhase != domain.RuntimeRunning {
			if session.RuntimePhase == domain.RuntimeDegraded {
				screen, renderErr := h.renderSessionCard(ctx, actor, ref, 0)
				if renderErr == nil {
					_, _ = h.editResponseCard(ctx, actor, message, screen)
				}
			}
			return
		}
		snapshot, err := h.renderSessionCardSnapshot(ctx, actor, ref, 0)
		if err != nil {
			return
		}
		settled := h.settleFromTranscript(ctx, actor, session, snapshot.events)
		if settled {
			snapshot, err = h.renderSessionCardSnapshot(ctx, actor, ref, 0)
			if err != nil {
				return
			}
		}
		preferences, preferencesErr := h.service.Preferences(actor)
		panePhase := session.RuntimePhase
		if settled {
			panePhase = domain.RuntimeIdle
		}
		if preferencesErr == nil && shouldAttachPane(preferences, panePhase) {
			h.attachPane(ctx, actor, ref, message, generation, attempt, &snapshot.screen)
		}
		if settled || (!message.Rich && (snapshot.screen.RichMarkdown || snapshot.screen.Pane != nil)) {
			message, err = h.editResponseCard(ctx, actor, message, snapshot.screen)
		} else {
			// Keep high-frequency live-pane edits local to this worker. Replicating
			// every screenshot hash would add avoidable Raft traffic; durable
			// transport metadata is needed only at a settled interaction boundary.
			message, err = h.messenger.EditScreen(ctx, message, snapshot.screen)
		}
		if err != nil {
			return
		}
		if settled {
			return
		}
		delay = paneRefreshDelay
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
	captureCtx, cancel := context.WithTimeout(ctx, paneCaptureLimit)
	defer cancel()
	pane, err := h.controls.CapturePane(
		captureCtx, actor,
		fmt.Sprintf("pane-%d-%d-%d", message.MessageID, generation, attempt), ref,
	)
	if err != nil {
		return
	}
	rendered, err := terminalimage.Render(string(pane), terminalimage.Options{})
	if err != nil {
		return
	}
	screen.Pane = &telegramui.PaneImage{
		PNG: rendered.PNG, Hash: rendered.Hash, AnchorOffset: len(screen.Text),
	}
}

func (h *Handler) attachImmediatePane(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	screen *telegramui.Screen,
) {
	captureCtx, cancel := context.WithTimeout(ctx, paneCaptureLimit)
	defer cancel()
	pane, err := h.controls.CapturePane(captureCtx, actor,
		fmt.Sprintf("pane-open-%d", time.Now().UnixNano()), ref)
	if err != nil {
		return
	}
	rendered, err := terminalimage.Render(string(pane), terminalimage.Options{})
	if err != nil {
		return
	}
	screen.Pane = &telegramui.PaneImage{
		PNG: rendered.PNG, Hash: rendered.Hash, AnchorOffset: len(screen.Text),
	}
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
	finalAt, ok := finalTranscriptAt(events)
	if !ok || !transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) {
		return false
	}
	settleCtx := application.WithOperationScope(
		ctx, fmt.Sprintf("transcript-final-%s-%d", session.Ref().Key(), finalAt.UnixNano()),
	)
	if h.service.PublishSessionRuntime(settleCtx, session, domain.RuntimeIdle, nil) != nil {
		return false
	}
	go h.deliverFinalFiles(ctx, actor, session.Ref(), events)
	return true
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
	// Older builds stamped LastEventAt again when the local node acknowledged
	// an already-delivered prompt. A fast backend can finish before that
	// acknowledgement is replicated. Accept only that narrow legacy skew; all
	// other finals older than the current turn remain stale.
	operation := session.LastOperation
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
