package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) RunEnrollmentNotifications(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if h.canRefresh() {
			h.scanEnrollmentNotifications(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) scanEnrollmentNotifications(ctx context.Context) {
	ownerID, pending := h.service.EnrollmentNotifications()
	if ownerID <= 0 {
		return
	}
	actor := application.Principal{UserID: ownerID}
	h.scanNewNodeSpeechSetup(ctx, actor)
	for _, request := range pending {
		screen, err := h.projector.EnrollmentDetail(actor, request.ID)
		if err != nil {
			continue
		}
		if _, err := h.messenger.SendScreen(ctx, int64(ownerID), screen); err != nil {
			continue
		}
		markCtx := application.WithOperationScope(ctx, "enrollment-notified-"+request.ID)
		_ = h.service.MarkEnrollmentNotified(markCtx, actor, request.ID)
	}
}

func (h *Handler) scanNewNodeSpeechSetup(ctx context.Context, actor application.Principal) {
	preferences, err := h.service.Preferences(actor)
	if err != nil || preferences.EffectiveVoiceBackend() == domain.VoiceOff {
		return
	}
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return
	}
	h.speechMu.Lock()
	if !h.speechNodesSeeded {
		for _, item := range nodes {
			h.knownSpeechNodes[item.Node.ID] = true
		}
		h.speechNodesSeeded = true
		h.speechMu.Unlock()
		return
	}
	h.speechMu.Unlock()
	for _, item := range nodes {
		if !item.Node.Enabled() || item.Node.Status == domain.NodeOffline {
			continue
		}
		h.speechMu.Lock()
		known := h.knownSpeechNodes[item.Node.ID]
		h.knownSpeechNodes[item.Node.ID] = true
		h.speechMu.Unlock()
		if known {
			continue
		}
		token, err := h.tokens.Node(
			actor.UserID, telegramui.ActionNodeSpeechSetup, item.Node.ID,
		)
		if err != nil {
			continue
		}
		_, _ = h.messenger.SendScreen(ctx, int64(actor.UserID),
			telegramui.RenderNewNodeVoiceSetup(h.copy(actor), item.Node.Name, token))
	}
}
