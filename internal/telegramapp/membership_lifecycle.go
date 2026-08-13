package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func isMembershipLifecycleAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionNodeDisable, telegramui.ActionNodeDisableYes,
		telegramui.ActionNodeEnable, telegramui.ActionNodeDelete,
		telegramui.ActionNodeDeleteYes, telegramui.ActionNodeRename:
		return true
	default:
		return false
	}
}

func (h *Handler) handleMembershipLifecycle(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
) error {
	if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
		return err
	}
	nodeID, err := h.resolveMembershipNode(actor, callback.Action, callback.Token)
	if err != nil {
		return nil
	}
	input, err := h.membershipNodeInput(actor, nodeID)
	if err != nil {
		return nil
	}
	switch callback.Action {
	case telegramui.ActionNodeDisable:
		if err := h.service.CanDisableNode(actor, nodeID); err != nil {
			return nil
		}
		return h.confirmNodeDisable(ctx, actor, nodeID, input, update.CallbackOrigin)
	case telegramui.ActionNodeDisableYes:
		if err := h.service.CanDisableNode(actor, nodeID); err != nil {
			return nil
		}
		go h.disableNode(actor, nodeID, input, update.CallbackOrigin)
		return nil
	case telegramui.ActionNodeEnable:
		if err := h.service.SetNodeEnabled(ctx, actor, nodeID, true); err != nil {
			return err
		}
		screen, err := h.projectNodeSettings(actor, nodeID)
		if err == nil {
			_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
		}
		return err
	case telegramui.ActionNodeDelete:
		return h.confirmNodeDelete(ctx, actor, nodeID, input, update.CallbackOrigin)
	case telegramui.ActionNodeDeleteYes:
		if err := h.service.DeleteNode(ctx, actor, nodeID); err != nil {
			return err
		}
		screen, err := h.projector.StatusMode(actor, telegramui.StatusSettings)
		if err == nil {
			_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
		}
		return err
	case telegramui.ActionNodeRename:
		h.beginNodeRename(actor, nodeID)
		back := h.nodeSettingsReturn(actor)
		_, err := h.messenger.EditScreen(ctx, update.CallbackOrigin,
			telegramui.RenderNodeRenamePromptWithBack(
				input.Copy, input.Node.Name, back.Action, back.Token,
			))
		return err
	default:
		return nil
	}
}

func (h *Handler) confirmNodeDisable(
	ctx context.Context, actor application.Principal, nodeID domain.NodeID,
	input telegramui.NodeMembershipInput, origin telegrambot.Message,
) error {
	confirm, err := h.tokens.Node(actor.UserID, telegramui.ActionNodeDisableYes, nodeID)
	if err == nil {
		_, err = h.messenger.EditScreen(ctx, origin,
			telegramui.RenderNodeDisableConfirmation(input, confirm))
	}
	return err
}

func (h *Handler) confirmNodeDelete(
	ctx context.Context, actor application.Principal, nodeID domain.NodeID,
	input telegramui.NodeMembershipInput, origin telegrambot.Message,
) error {
	confirm, err := h.tokens.Node(actor.UserID, telegramui.ActionNodeDeleteYes, nodeID)
	if err == nil {
		_, err = h.messenger.EditScreen(ctx, origin,
			telegramui.RenderNodeDeleteConfirmation(input, confirm))
	}
	return err
}

func (h *Handler) resolveMembershipNode(
	actor application.Principal, action telegramui.Action, token telegramui.OpaqueToken,
) (domain.NodeID, error) {
	candidates, err := h.service.CallbackNodeCandidates(actor)
	if err != nil {
		return "", err
	}
	return h.tokens.ResolveNode(actor.UserID, action, token, candidates)
}

func (h *Handler) membershipNodeInput(
	actor application.Principal, nodeID domain.NodeID,
) (telegramui.NodeMembershipInput, error) {
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return telegramui.NodeMembershipInput{}, err
	}
	for _, item := range nodes {
		if item.Node.ID == nodeID {
			back := h.nodeSettingsReturn(actor)
			return telegramui.NodeMembershipInput{
				Copy: h.copy(actor), Node: item.Node, LiveSessions: item.LiveSessions,
				BackAction: back.Action, BackToken: back.Token,
			}, nil
		}
	}
	return telegramui.NodeMembershipInput{}, domain.ErrNotFound
}

func (h *Handler) disableNode(
	actor application.Principal, nodeID domain.NodeID,
	input telegramui.NodeMembershipInput, origin telegrambot.Message,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if h.service.CanDisableNode(actor, nodeID) != nil {
		_, _ = h.messenger.EditScreen(ctx, origin,
			h.nodeSettingsResult(actor, i18n.ToastUnavailable, ""))
		return
	}
	errorsFound := h.nodeDisableErrors(ctx, actor, nodeID, input.Node.Status)
	disableCtx, disableCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer disableCancel()
	if err := h.service.SetNodeEnabled(disableCtx, actor, nodeID, false); err != nil {
		_, _ = h.messenger.EditScreen(disableCtx, origin,
			h.nodeSettingsResult(actor, i18n.ToastUnavailable, ""))
		return
	}
	key, detail := i18n.NodeDisabled, ""
	if len(errorsFound) > 0 {
		key, detail = i18n.NodeDisabledWithErrors, strings.Join(errorsFound, "; ")
	}
	_, _ = h.messenger.EditScreen(disableCtx, origin,
		h.nodeSettingsResult(actor, key, detail))
}

func (h *Handler) nodeDisableErrors(
	ctx context.Context, actor application.Principal, nodeID domain.NodeID, status domain.NodeStatus,
) []string {
	if status != domain.NodeOffline {
		return h.closeNodeSessions(ctx, actor, nodeID)
	}
	sessions, err := h.service.LiveSessionsOnNode(actor, nodeID)
	if err != nil {
		return []string{err.Error()}
	}
	result := make([]string, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, sessionLabel(session))
	}
	return result
}

func (h *Handler) closeNodeSessions(
	ctx context.Context, actor application.Principal, nodeID domain.NodeID,
) []string {
	if h.controls == nil {
		return []string{"session controls unavailable"}
	}
	sessions, err := h.service.LiveSessionsOnNode(actor, nodeID)
	if err != nil {
		return []string{err.Error()}
	}
	failed := make([]string, 0)
	for index, session := range sessions {
		ref := session.Ref()
		op := fmt.Sprintf("disable-%s-%d-%d", nodeID, time.Now().UnixNano(), index)
		if session.RuntimePhase != domain.RuntimeIdle && session.RuntimePhase != domain.RuntimeWaitingInput {
			if _, err := h.controls.Stop(ctx, actor, op+"-stop", ref); err != nil ||
				!h.waitUntilClosable(ctx, actor, ref) {
				failed = append(failed, sessionLabel(session))
				continue
			}
		}
		accepted, err := h.controls.Close(ctx, actor, op+"-close", ref)
		if err != nil || accepted.Deferred {
			failed = append(failed, sessionLabel(session))
		}
	}
	return failed
}

func sessionLabel(session domain.Session) string {
	if strings.TrimSpace(session.Name) != "" {
		return session.Name
	}
	return session.Ref().Key()
}

func (h *Handler) waitUntilClosable(
	ctx context.Context, actor application.Principal, ref domain.SessionRef,
) bool {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
			session, err := h.service.Session(actor, ref)
			if errors.Is(err, domain.ErrNotFound) {
				return true
			}
			if err == nil && (session.RuntimePhase == domain.RuntimeIdle ||
				session.RuntimePhase == domain.RuntimeWaitingInput) {
				return true
			}
		}
	}
}
