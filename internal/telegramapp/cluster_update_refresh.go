package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
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
	if h.clusterUpdater == nil || message.ChatID <= 0 || message.MessageID <= 0 {
		return
	}
	update, _, err := h.service.ClusterUpdate(actor)
	if err != nil || update == nil || !update.Active() {
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
	go h.refreshClusterUpdateCard(workerCtx, actor, message, initial.Text, update.ID, generation)
}

func (h *Handler) refreshClusterUpdateCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	lastText string,
	updateID string,
	generation uint64,
) {
	defer h.finishClusterUpdateRefresh(actor.UserID, updateID, generation)
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(45 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-timeout.C:
			return
		case <-ticker.C:
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
			continue
		}
		h.cardEditMu.Lock()
		if !h.clusterUpdateRefreshCurrent(actor.UserID, generation) {
			h.cardEditMu.Unlock()
			return
		}
		edited, editErr := h.messenger.EditScreen(ctx, message, screen)
		if editErr == nil {
			message = edited
			h.rememberResponseCard(ctx, actor, edited, screen)
			lastText = screen.Text
		}
		h.cardEditMu.Unlock()
		if !update.Active() && editErr == nil {
			return
		}
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
		update, nodes, updateErr := h.service.ClusterUpdate(actor)
		if updateErr != nil || update == nil || !update.Active() {
			continue
		}
		screen := h.renderClusterUpdate(ctx, actor, *update, nodes)
		h.scheduleClusterUpdateRefresh(ctx, actor, telegramMessage(card), screen)
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
