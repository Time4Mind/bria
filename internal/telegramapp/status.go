package telegramapp

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type leadershipTransfer interface {
	LeaderID() string
	TransferLeadershipTo(string) error
}

type settingsReturn struct {
	Action telegramui.Action
	Token  telegramui.OpaqueToken
}

func isStatusAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionStatus, telegramui.ActionStatusRefresh, telegramui.ActionStatusMode,
		telegramui.ActionStatusLeaderNode, telegramui.ActionStatusSettingsNode,
		telegramui.ActionNodeSettings, telegramui.ActionConfirmLeader:
		return true
	default:
		return false
	}
}

func (h *Handler) openNodeSettingsFromSessions(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionNodeSettings, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	h.rememberNodeSettingsReturn(actor, nodeID, true)
	return h.projectNodeSettings(actor, nodeID)
}

func (h *Handler) openLegacyNodeSettingsFromSessions(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionStatusSettingsNode, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	h.rememberNodeSettingsReturn(actor, nodeID, true)
	return h.projectNodeSettings(actor, nodeID)
}

func statusMode(token telegramui.OpaqueToken) telegramui.StatusMode {
	switch telegramui.StatusMode(token) {
	case telegramui.StatusLeader:
		return telegramui.StatusLeader
	case telegramui.StatusSettings:
		return telegramui.StatusSettings
	default:
		return telegramui.StatusChoose
	}
}

func (h *Handler) openStatus(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	mode telegramui.StatusMode,
	refresh bool,
) (telegramui.Screen, error) {
	if refresh {
		if err := h.service.RequestQuotaRefresh(ctx, actor); err != nil {
			return telegramui.Screen{}, err
		}
	}
	return h.projector.StatusMode(actor, mode)
}

func (h *Handler) confirmStatusLeader(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionStatusLeaderNode, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return h.projector.ConfirmLeader(actor, nodeID)
}

func (h *Handler) openStatusNodeSettings(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionStatusSettingsNode, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	h.rememberNodeSettingsReturn(actor, nodeID, false)
	return h.projectNodeSettings(actor, nodeID)
}

func (h *Handler) rememberNodeSettingsReturn(
	actor application.Principal,
	nodeID domain.NodeID,
	fromSessions bool,
) {
	back := settingsReturn{Action: telegramui.ActionStatusMode, Token: "settings"}
	if fromSessions {
		if token, err := h.tokens.Node(actor.UserID, telegramui.ActionSelectNode, nodeID); err == nil {
			back = settingsReturn{Action: telegramui.ActionSelectNode, Token: token}
		}
	}
	h.membershipMu.Lock()
	h.nodeSettingsBack[actor.UserID] = back
	h.membershipMu.Unlock()
}

func (h *Handler) nodeSettingsReturn(actor application.Principal) settingsReturn {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	back, ok := h.nodeSettingsBack[actor.UserID]
	if !ok {
		return settingsReturn{Action: telegramui.ActionStatusMode, Token: "settings"}
	}
	return back
}

func (h *Handler) projectNodeSettings(
	actor application.Principal,
	nodeID domain.NodeID,
) (telegramui.Screen, error) {
	back := h.nodeSettingsReturn(actor)
	return h.projector.NodeSettingsWithReturn(actor, nodeID, back.Action, back.Token)
}

func (h *Handler) nodeSettingsResult(
	actor application.Principal,
	key i18n.Key,
	detail string,
) telegramui.Screen {
	back := h.nodeSettingsReturn(actor)
	return telegramui.RenderMembershipResultWithBack(
		h.copy(actor), key, detail, back.Action, back.Token,
	)
}

func (h *Handler) applyStatusLeader(
	ctx context.Context,
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionConfirmLeader, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if err := h.service.SetTemporaryLeader(ctx, actor, nodeID); err != nil {
		return telegramui.Screen{}, err
	}
	transfer, ok := h.leadership.(leadershipTransfer)
	if !ok {
		return telegramui.Screen{}, errors.New("leadership transfer is unavailable")
	}
	if transfer.LeaderID() != string(nodeID) {
		if err := transfer.TransferLeadershipTo(string(nodeID)); err != nil {
			return telegramui.Screen{}, err
		}
	}
	return h.projector.StatusMode(actor, telegramui.StatusChoose)
}

func (h *Handler) resolveStatusNode(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (domain.NodeID, error) {
	candidates, err := h.service.CallbackNodeCandidates(actor)
	if err != nil {
		return "", err
	}
	return h.tokens.ResolveNode(actor.UserID, action, token, candidates)
}

func (h *Handler) scheduleStatusRefresh(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	mode telegramui.StatusMode,
) {
	if message.ChatID == 0 || message.MessageID == 0 {
		return
	}
	initial := h.service.StatusFingerprint(actor)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(22 * time.Second)
		defer timeout.Stop()
		last := initial
		updated := false
		quietSince := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-timeout.C:
				return
			case <-ticker.C:
				if !h.canRefresh() {
					return
				}
				current := h.service.StatusFingerprint(actor)
				if current == last {
					if updated && time.Since(quietSince) >= 2*time.Second {
						return
					}
					continue
				}
				screen, err := h.projector.StatusMode(actor, mode)
				if err == nil {
					_, _ = h.messenger.EditScreen(ctx, message, screen)
				}
				last = current
				updated = true
				quietSince = time.Now()
			}
		}
	}()
}
