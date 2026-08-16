package telegramapp

import (
	"context"
	"html"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type providerAuthFlow struct {
	NodeID    domain.NodeID
	Backend   string
	FlowID    string
	ExpiresAt time.Time
	Carrier   telegrambot.Message
	Code      bool
}

func (h *Handler) handleProviderAuthCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
) error {
	if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
		return err
	}
	if callback.Action == telegramui.ActionProviderAuthCancel {
		return h.cancelProviderAuth(ctx, actor, update.CallbackOrigin, callback.Token)
	}
	if h.providerAuth == nil {
		_, err := h.messenger.EditScreen(ctx, update.CallbackOrigin,
			h.nodeSettingsResult(actor, i18n.ToastUnavailable, ""))
		return err
	}
	nodeID, backend, err := h.resolveProviderAuthChoice(actor, callback.Token)
	if err != nil {
		return nil
	}
	backToken, err := h.nodeBackendDetailToken(actor, nodeID, backend)
	if err != nil {
		return err
	}
	starting := telegramui.RenderProviderAuthStartingWithBack(
		h.copy(actor), backend, telegramui.ActionNodeBackend, backToken,
	)
	carrier, err := h.messenger.EditScreen(ctx, update.CallbackOrigin, starting)
	if err != nil {
		return err
	}
	pending := providerAuthFlow{
		NodeID: nodeID, Backend: backend, ExpiresAt: time.Now().Add(10 * time.Minute), Carrier: carrier,
	}
	h.membershipMu.Lock()
	h.providerAuthFlows[actor.UserID] = pending
	h.membershipMu.Unlock()
	status, err := h.providerAuth.Start(ctx, providerauth.StartRequest{
		ActorID: int64(actor.UserID), NodeID: string(nodeID), Backend: backend,
	})
	if err != nil {
		return h.editProviderAuthResult(ctx, actor, carrier, i18n.ProviderAuthFailed, "unavailable")
	}
	flow := providerAuthFlow{
		NodeID: nodeID, Backend: backend, FlowID: status.FlowID,
		ExpiresAt: status.ExpiresAt, Carrier: carrier,
		Code: status.State == providerauth.StateWaitingInput,
	}
	h.membershipMu.Lock()
	h.providerAuthFlows[actor.UserID] = flow
	h.membershipMu.Unlock()
	cancelToken, err := h.tokens.Choice(
		actor.UserID, telegramui.ActionProviderAuthCancel, "provider_auth_cancel", status.FlowID,
	)
	if err != nil {
		_ = h.providerAuth.Cancel(ctx, flow.request(actor))
		return err
	}
	screen := telegramui.RenderProviderAuthChallenge(
		h.copy(actor), backend, status.URL, status.UserCode, flow.Code, cancelToken,
	)
	carrier, err = h.messenger.EditScreen(ctx, carrier, screen)
	if err != nil {
		_ = h.providerAuth.Cancel(ctx, flow.request(actor))
		return err
	}
	h.membershipMu.Lock()
	flow.Carrier = carrier
	h.providerAuthFlows[actor.UserID] = flow
	h.membershipMu.Unlock()
	if !flow.Code {
		go h.watchProviderAuth(context.WithoutCancel(ctx), actor, flow)
	}
	return nil
}

func (h *Handler) resolveProviderAuthChoice(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (domain.NodeID, string, error) {
	available, err := h.service.ProviderAliasCandidates(actor)
	if err != nil {
		return "", "", err
	}
	candidates := make([]string, 0, len(available))
	for _, candidate := range available {
		candidates = append(candidates, string(candidate.NodeID)+"\x00"+candidate.Backend)
	}
	choice, err := h.tokens.ResolveChoice(
		actor.UserID, telegramui.ActionProviderAuth, "provider_auth", token, candidates,
	)
	if err != nil {
		return "", "", err
	}
	nodeID, backend, ok := strings.Cut(choice, "\x00")
	if !ok {
		return "", "", domain.ErrNotFound
	}
	return domain.NodeID(nodeID), backend, nil
}

func (h *Handler) awaitingProviderAuthCode(actor application.Principal) bool {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	flow, ok := h.providerAuthFlows[actor.UserID]
	if !ok || !flow.Code || !time.Now().Before(flow.ExpiresAt) {
		if ok && !time.Now().Before(flow.ExpiresAt) {
			delete(h.providerAuthFlows, actor.UserID)
		}
		return false
	}
	return true
}

func (h *Handler) acceptProviderAuthCode(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	code string,
) error {
	if code == "" || strings.HasPrefix(code, "/") {
		return nil
	}
	h.membershipMu.Lock()
	flow, ok := h.providerAuthFlows[actor.UserID]
	h.membershipMu.Unlock()
	if !ok || h.providerAuth == nil {
		return nil
	}
	_ = h.messenger.DeleteMessage(ctx, telegrambot.Message{
		ChatID: update.ChatID, MessageID: update.MessageID,
	})
	_, err := h.providerAuth.Submit(ctx, providerauth.SubmitRequest{
		ActorID: int64(actor.UserID), NodeID: string(flow.NodeID), FlowID: flow.FlowID, Code: code,
	})
	if err != nil {
		return h.editProviderAuthResult(ctx, actor, flow.Carrier, i18n.ProviderAuthFailed, "rejected")
	}
	flow.Code = false
	h.membershipMu.Lock()
	h.providerAuthFlows[actor.UserID] = flow
	h.membershipMu.Unlock()
	go h.watchProviderAuth(context.WithoutCancel(ctx), actor, flow)
	return nil
}

func (h *Handler) watchProviderAuth(
	ctx context.Context,
	actor application.Principal,
	flow providerAuthFlow,
) {
	watchCtx, cancel := context.WithDeadline(ctx, flow.ExpiresAt.Add(5*time.Second))
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for time.Now().Before(flow.ExpiresAt) {
		select {
		case <-watchCtx.Done():
			return
		case <-ticker.C:
			status, err := h.providerAuth.Status(watchCtx, flow.request(actor))
			if err != nil {
				continue
			}
			if !status.State.Terminal() {
				continue
			}
			key, detail := i18n.ProviderAuthSucceeded, ""
			if status.State != providerauth.StateSucceeded {
				key, detail = i18n.ProviderAuthFailed, status.Detail
			}
			_ = h.editProviderAuthResult(ctx, actor, flow.Carrier, key, detail)
			return
		}
	}
	_ = h.editProviderAuthResult(ctx, actor, flow.Carrier, i18n.ProviderAuthFailed, "expired")
}

func (h *Handler) cancelProviderAuth(
	ctx context.Context,
	actor application.Principal,
	carrier telegrambot.Message,
	token telegramui.OpaqueToken,
) error {
	h.membershipMu.Lock()
	flow, ok := h.providerAuthFlows[actor.UserID]
	if ok {
		_, resolveErr := h.tokens.ResolveChoice(
			actor.UserID, telegramui.ActionProviderAuthCancel, "provider_auth_cancel",
			token, []string{flow.FlowID},
		)
		ok = resolveErr == nil
	}
	if !ok {
		h.membershipMu.Unlock()
		return nil
	}
	h.membershipMu.Unlock()
	if ok && h.providerAuth != nil {
		_ = h.providerAuth.Cancel(ctx, flow.request(actor))
		carrier = flow.Carrier
	}
	return h.editProviderAuthResult(ctx, actor, carrier, i18n.ProviderAuthCancelled, "")
}

func (h *Handler) cancelProviderAuthFlow(
	ctx context.Context,
	actor application.Principal,
) {
	h.membershipMu.Lock()
	flow, ok := h.providerAuthFlows[actor.UserID]
	delete(h.providerAuthFlows, actor.UserID)
	h.membershipMu.Unlock()
	if ok && h.providerAuth != nil {
		_ = h.providerAuth.Cancel(ctx, flow.request(actor))
	}
}

func (h *Handler) editProviderAuthResult(
	ctx context.Context,
	actor application.Principal,
	carrier telegrambot.Message,
	key i18n.Key,
	detail string,
) error {
	h.membershipMu.Lock()
	flow := h.providerAuthFlows[actor.UserID]
	delete(h.providerAuthFlows, actor.UserID)
	h.membershipMu.Unlock()
	text := h.copy(actor).Text(key)
	if detail != "" {
		text = h.copy(actor).Format(key, html.EscapeString(detail))
	}
	back := h.nodeSettingsReturn(actor)
	if flow.NodeID != "" && flow.Backend != "" {
		if token, tokenErr := h.nodeBackendDetailToken(actor, flow.NodeID, flow.Backend); tokenErr == nil {
			back = settingsReturn{Action: telegramui.ActionNodeBackend, Token: token}
		}
	}
	_, err := h.messenger.EditScreen(ctx, carrier, telegramui.Screen{
		Name: telegramui.ScreenStatus, ParseMode: telegramui.ParseModeHTML,
		Text: text, Grid: telegramui.Grid{telegramui.Row{{
			Label:    h.copy(actor).Text(i18n.ButtonBack),
			Callback: telegramui.Callback{Action: back.Action, Token: back.Token},
		}}},
	})
	return err
}

func (f providerAuthFlow) request(actor application.Principal) providerauth.FlowRequest {
	return providerauth.FlowRequest{
		ActorID: int64(actor.UserID), NodeID: string(f.NodeID), FlowID: f.FlowID,
	}
}
