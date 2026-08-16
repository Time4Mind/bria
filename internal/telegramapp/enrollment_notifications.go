package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/speechsetup"
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
	for _, item := range nodes {
		if !item.Node.Enabled() || item.Node.Status == domain.NodeOffline {
			continue
		}
		h.speechMu.Lock()
		known := h.knownSpeechNodes[item.Node.ID]
		h.speechMu.Unlock()
		if known || h.speechSetup == nil {
			continue
		}
		request := speechsetup.Request{NodeID: string(item.Node.ID)}
		status, err := h.speechSetup.Status(ctx, request)
		if err != nil {
			continue
		}
		notify := status.Phase == speechsetup.PhasePermissionRequired
		if status.Phase == speechsetup.PhaseReady || status.Phase == speechsetup.PhaseInstalling {
			h.markSpeechNodeKnown(item.Node.ID)
			continue
		}
		if !notify {
			status, err = h.speechSetup.Start(ctx, request)
			if err != nil {
				continue
			}
		}
		h.markSpeechNodeKnown(item.Node.ID)
		_, _ = h.messenger.SendScreen(ctx, int64(actor.UserID), telegramui.RenderVoiceSetupStarted(
			h.copy(actor), []string{item.Node.Name + ": " + speechStatusText(status)},
		))
	}
}

func (h *Handler) markSpeechNodeKnown(nodeID domain.NodeID) {
	h.speechMu.Lock()
	h.knownSpeechNodes[nodeID] = true
	h.speechMu.Unlock()
}
