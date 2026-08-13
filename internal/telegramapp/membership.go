package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func isEnrollmentAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionClusterAdd, telegramui.ActionClusterInvite,
		telegramui.ActionClusterContract, telegramui.ActionEnrollmentOpen,
		telegramui.ActionEnrollmentApprove, telegramui.ActionEnrollmentReject:
		return true
	default:
		return false
	}
}

func (h *Handler) createEnrollmentInvitation(
	ctx context.Context,
	actor application.Principal,
) (telegramui.Screen, error) {
	invitation, _, err := h.service.CreateEnrollmentInvitation(ctx, actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderClusterInvitation(h.copy(actor), invitation), nil
}

func (h *Handler) beginNodeContract(actor application.Principal) telegramui.Screen {
	h.membershipMu.Lock()
	delete(h.renameFlows, actor.UserID)
	delete(h.providerAliasFlows, actor.UserID)
	h.contractFlows[actor.UserID] = time.Now().Add(10 * time.Minute)
	h.membershipMu.Unlock()
	return telegramui.RenderNodeContractPrompt(h.copy(actor))
}

func (h *Handler) clearMembershipFlows(actor application.Principal) {
	h.membershipMu.Lock()
	delete(h.contractFlows, actor.UserID)
	delete(h.renameFlows, actor.UserID)
	delete(h.providerAliasFlows, actor.UserID)
	delete(h.nodeSettingsBack, actor.UserID)
	h.membershipMu.Unlock()
}

func (h *Handler) awaitingNodeContract(actor application.Principal) bool {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	deadline, ok := h.contractFlows[actor.UserID]
	if !ok || !time.Now().Before(deadline) {
		delete(h.contractFlows, actor.UserID)
		return false
	}
	return true
}

func (h *Handler) acceptNodeContract(
	ctx context.Context,
	actor application.Principal,
	chatID int64,
	value string,
) error {
	request, err := h.service.SubmitNodeContract(ctx, actor, value)
	if err != nil {
		screen := telegramui.RenderMembershipResult(
			h.copy(actor), i18n.ToastUnavailable, "",
		)
		return h.sendProjected(ctx, chatID, screen, nil)
	}
	h.membershipMu.Lock()
	delete(h.contractFlows, actor.UserID)
	h.membershipMu.Unlock()
	screen, err := h.projector.EnrollmentDetail(actor, request.ID)
	return h.sendProjected(ctx, chatID, screen, err)
}

func (h *Handler) openEnrollment(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	requestID, err := h.resolveEnrollment(actor, action, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return h.projector.EnrollmentDetail(actor, requestID)
}

func (h *Handler) decideEnrollment(
	ctx context.Context,
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	requestID, err := h.resolveEnrollment(actor, action, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	approve := action == telegramui.ActionEnrollmentApprove
	request, err := h.service.EnrollmentRequest(actor, requestID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if err := h.service.DecideEnrollment(ctx, actor, requestID, approve); err != nil {
		return telegramui.Screen{}, err
	}
	key := i18n.EnrollmentRejectedNotice
	if approve {
		key = i18n.EnrollmentApprovedNotice
		if request.InviteID == "" {
			claim, claimErr := h.service.EnrollmentClaim(actor, requestID)
			if claimErr != nil {
				return telegramui.Screen{}, claimErr
			}
			return telegramui.RenderEnrollmentClaim(h.copy(actor), claim), nil
		}
	}
	return telegramui.RenderMembershipResult(h.copy(actor), key, ""), nil
}

func (h *Handler) resolveEnrollment(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (string, error) {
	pending, err := h.service.PendingEnrollments(actor)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0, len(pending))
	for _, request := range pending {
		candidates = append(candidates, request.ID)
	}
	return h.tokens.ResolveChoice(actor.UserID, action, "enrollment", token, candidates)
}
