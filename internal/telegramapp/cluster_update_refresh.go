package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) cancelClusterUpdateRefresh(userID domain.UserID) {
	h.clusterUpdateMu.Lock()
	if h.clusterUpdateWatch == nil {
		h.clusterUpdateWatch = make(map[domain.UserID]uint64)
	}
	h.clusterUpdateWatch[userID]++
	delete(h.clusterUpdateJobs, userID)
	h.clusterUpdateMu.Unlock()
}

func (h *Handler) scheduleClusterUpdateRefresh(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	initial telegramui.Screen,
) {
	h.scheduleClusterUpdateRefreshFrom(ctx, actor, message, initial.Text, false)
}

func (h *Handler) scheduleRestoredClusterUpdateRefresh(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
) {
	// The replicated card stores a fingerprint, not its rendered text. Force one
	// comparison/edit after restart so a rollout that completed before adapter
	// recovery still publishes its terminal state on the existing carrier.
	h.scheduleClusterUpdateRefreshFrom(ctx, actor, message, "", true)
}

func (h *Handler) scheduleClusterUpdateRefreshFrom(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	lastText string,
	restored bool,
) {
	if h.clusterUpdater == nil || message.ChatID <= 0 || message.MessageID <= 0 {
		return
	}
	update, _, err := h.service.ClusterUpdate(actor)
	if err != nil || update == nil || (!restored && !update.Active()) {
		return
	}
	h.clusterUpdateMu.Lock()
	if h.clusterUpdateWatch == nil {
		h.clusterUpdateWatch = make(map[domain.UserID]uint64)
	}
	if h.clusterUpdateJobs == nil {
		h.clusterUpdateJobs = make(map[domain.UserID]string)
	}
	if h.clusterUpdateJobs[actor.UserID] == update.ID {
		h.clusterUpdateMu.Unlock()
		return
	}
	h.clusterUpdateWatch[actor.UserID]++
	generation := h.clusterUpdateWatch[actor.UserID]
	h.clusterUpdateJobs[actor.UserID] = update.ID
	h.clusterUpdateMu.Unlock()
	workerCtx := context.WithoutCancel(ctx)
	go h.refreshClusterUpdateCard(
		workerCtx, actor, message, lastText, update.ID, generation, restored,
	)
}

func (h *Handler) refreshClusterUpdateCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	lastText string,
	updateID string,
	generation uint64,
	restored bool,
) {
	defer h.finishClusterUpdateRefresh(actor.UserID, updateID, generation)
	next := time.NewTimer(0)
	defer next.Stop()
	timeout := time.NewTimer(45 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-timeout.C:
			return
		case <-next.C:
		}
		if !h.clusterUpdateRefreshCurrent(actor.UserID, generation) || !h.canRefresh() {
			return
		}
		update, nodes, err := h.service.ClusterUpdate(actor)
		if err != nil || update == nil {
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		screen := h.renderClusterUpdate(requestCtx, actor, *update, nodes)
		cancel()
		if screen.Text == lastText {
			if !update.Active() {
				return
			}
			next.Reset(750 * time.Millisecond)
			continue
		}
		release, acquireErr := h.responseCards.acquire(ctx, actor.UserID)
		if acquireErr != nil {
			return
		}
		if !h.clusterUpdateRefreshCurrent(actor.UserID, generation) {
			release()
			return
		}
		edited, editErr := h.messenger.EditScreen(ctx, message, screen)
		if editErr == nil {
			message = edited
			h.rememberResponseCardCoordinated(ctx, actor, edited, screen)
			lastText = screen.Text
		}
		release()
		if !update.Active() && editErr == nil {
			if restored {
				processlog.Detailf(
					"bria telegram: cluster_update_card update=%q outcome=restored phase=%s",
					update.ID, update.Phase,
				)
			}
			return
		}
		next.Reset(750 * time.Millisecond)
	}
}

func (h *Handler) restoreClusterUpdateRefreshes(ctx context.Context) {
	for _, userID := range h.service.BackgroundPanelUsers() {
		if h.hasClusterUpdateRefresh(userID) {
			continue
		}
		actor := application.Principal{UserID: userID}
		card, ok, cardErr := h.service.TelegramResponseCard(actor)
		if cardErr != nil || !ok || card.PaneHash != clusterUpdateCardMarker {
			continue
		}
		update, _, updateErr := h.service.ClusterUpdate(actor)
		if updateErr != nil || update == nil {
			continue
		}
		h.scheduleRestoredClusterUpdateRefresh(ctx, actor, telegramMessage(card))
	}
}

func (h *Handler) hasClusterUpdateRefresh(userID domain.UserID) bool {
	h.clusterUpdateMu.Lock()
	defer h.clusterUpdateMu.Unlock()
	return h.clusterUpdateJobs[userID] != ""
}

func (h *Handler) finishClusterUpdateRefresh(
	userID domain.UserID, updateID string, generation uint64,
) {
	h.clusterUpdateMu.Lock()
	defer h.clusterUpdateMu.Unlock()
	if h.clusterUpdateWatch[userID] == generation && h.clusterUpdateJobs[userID] == updateID {
		delete(h.clusterUpdateJobs, userID)
	}
}

func (h *Handler) clusterUpdateRefreshCurrent(userID domain.UserID, generation uint64) bool {
	h.clusterUpdateMu.Lock()
	defer h.clusterUpdateMu.Unlock()
	return h.clusterUpdateWatch[userID] == generation
}
