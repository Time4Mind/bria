package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/interactive"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const interactiveActionTimeout = 5 * time.Second

func isInteractiveAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionKeyUp, telegramui.ActionKeyDown, telegramui.ActionKeyLeft,
		telegramui.ActionKeyRight, telegramui.ActionKeyEnter, telegramui.ActionKeyEscape,
		telegramui.ActionKeySpace, telegramui.ActionKeyTab, telegramui.ActionKeyCtrlC,
		telegramui.ActionKeyBack:
		return true
	default:
		return false
	}
}

func (h *Handler) renderInteractiveSessionCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
) (telegramui.Screen, bool, error) {
	session, err := h.service.Session(actor, ref)
	if err != nil || session.InteractivePrompt == nil || h.controls == nil {
		return telegramui.Screen{}, false, err
	}
	captureCtx, cancel := context.WithTimeout(ctx, paneForegroundWait)
	defer cancel()
	pane, err := h.capturePaneCoalesced(captureCtx, actor, ref,
		fmt.Sprintf("interactive-%d-%d", actor.UserID, time.Now().UnixNano()))
	if err != nil {
		return telegramui.Screen{}, false, nil
	}
	prompt, ok := interactive.Detect(pane)
	if !ok || prompt.Hash != session.InteractivePrompt.Hash {
		return telegramui.Screen{}, false, nil
	}
	screen, err := h.interactiveScreen(actor, ref, prompt)
	return screen, err == nil, err
}

func (h *Handler) interactiveScreen(
	actor application.Principal,
	ref domain.SessionRef,
	prompt interactive.Prompt,
) (telegramui.Screen, error) {
	base, err := h.projector.SessionCardPageWithContext(
		actor, ref, nil, 0, application.CardContext{HideBackground: true},
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	control := h.service.RequireSessionAction(actor, ref, domain.ActionSendKey) == nil
	tokens := make(map[telegramui.Action]telegramui.OpaqueToken)
	for _, action := range interactiveActions(control, prompt.VerticalOnly()) {
		token, tokenErr := h.tokens.Interactive(actor.UserID, action, ref, prompt.Hash)
		if tokenErr != nil {
			return telegramui.Screen{}, tokenErr
		}
		tokens[action] = token
	}
	h.rememberPrompt(actor.UserID, ref, prompt.Hash)
	screen := telegramui.RenderInteractiveCard(telegramui.InteractiveInput{
		Copy: h.copy(actor), Text: base.Text + "\n\n" +
			telegramui.FormatInteractivePrompt(prompt.Content),
		Control: control, VerticalOnly: prompt.VerticalOnly(), Tokens: tokens,
	})
	screen.Checkpoint = base.Checkpoint
	return screen, nil
}

func interactiveActions(control, verticalOnly bool) []telegramui.Action {
	actions := []telegramui.Action{telegramui.ActionKeyBack}
	if !control {
		return actions
	}
	actions = append(actions, telegramui.ActionKeyDown, telegramui.ActionKeyEscape,
		telegramui.ActionKeyCtrlC, telegramui.ActionKeyEnter, telegramui.ActionKeyUp,
		telegramui.ActionKeySpace, telegramui.ActionKeyTab)
	if !verticalOnly {
		actions = append(actions, telegramui.ActionKeyLeft, telegramui.ActionKeyRight)
	}
	return actions
}

func (h *Handler) handleInteractiveCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
) error {
	if h.controls == nil {
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	target, err := h.resolveInteractive(actor, callback.Action, callback.Token)
	if err != nil {
		return h.controlError(ctx, actor, update.CallbackID, err)
	}
	if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
		return err
	}
	if callback.Action == telegramui.ActionKeyBack {
		screen, renderErr := h.renderRegularSessionCard(ctx, actor, target.Session, 0)
		if renderErr != nil {
			return renderErr
		}
		var edited telegrambot.Message
		edited, err = h.editExplicitSessionScreen(ctx, actor, update.CallbackOrigin, screen)
		if err == nil {
			h.rememberInteractiveCarrier(ctx, actor, edited, screen)
			h.schedulePaneRefresh(ctx, actor, target.Session, edited)
		}
		return err
	}
	key, ok := interactiveKey(callback.Action)
	if !ok {
		return nil
	}
	actionCtx, cancel := context.WithTimeout(ctx, interactiveActionTimeout)
	defer cancel()
	pane, err := h.controls.SendKey(
		actionCtx, actor, fmt.Sprintf("tg-%d-%s", update.UpdateID, callback.Action),
		target.Session, key, target.PromptHash,
	)
	if err != nil {
		if errors.Is(err, domain.ErrStaleOperation) ||
			errors.Is(err, runtimehost.ErrStaleRuntime) ||
			errors.Is(err, runtimehost.ErrRuntimeUnavailable) ||
			errors.Is(err, sessioncontrol.ErrRuntimeUnavailable) ||
			errors.Is(err, context.DeadlineExceeded) {
			screen, renderErr := h.renderSessionCard(ctx, actor, target.Session, 0)
			if renderErr == nil {
				if edited, editErr := h.editExplicitSessionScreen(
					ctx, actor, update.CallbackOrigin, screen,
				); editErr == nil {
					h.rememberInteractiveCarrier(ctx, actor, edited, screen)
					h.schedulePaneRefresh(ctx, actor, target.Session, edited)
				}
			}
			return nil
		}
		return err
	}
	prompt, waiting := interactive.Detect(pane)
	var screen telegramui.Screen
	if waiting {
		screen, err = h.interactiveScreen(actor, target.Session, prompt)
	} else {
		screen, err = h.renderRegularSessionCard(ctx, actor, target.Session, 0)
	}
	if err != nil {
		return err
	}
	message, err := h.editExplicitSessionScreen(ctx, actor, update.CallbackOrigin, screen)
	if err == nil {
		h.rememberInteractiveCarrier(ctx, actor, message, screen)
		if !waiting {
			h.schedulePaneRefresh(ctx, actor, target.Session, message)
		}
	}
	return err
}

// An automatic waiting-input carrier may have appeared while a menu was the
// visible response card. Once the user acts on that carrier it becomes the
// authoritative session screen, so later pane refreshes target it normally.
func (h *Handler) rememberInteractiveCarrier(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	screen telegramui.Screen,
) {
	h.beginVisibleScreen(actor.UserID, screen)
	h.rememberResponseCard(ctx, actor, message, screen)
}

func interactiveKey(action telegramui.Action) (runtimehost.InteractiveKey, bool) {
	switch action {
	case telegramui.ActionKeyUp:
		return runtimehost.KeyUp, true
	case telegramui.ActionKeyDown:
		return runtimehost.KeyDown, true
	case telegramui.ActionKeyLeft:
		return runtimehost.KeyLeft, true
	case telegramui.ActionKeyRight:
		return runtimehost.KeyRight, true
	case telegramui.ActionKeyEnter:
		return runtimehost.KeyEnter, true
	case telegramui.ActionKeyEscape:
		return runtimehost.KeyEscape, true
	case telegramui.ActionKeySpace:
		return runtimehost.KeySpace, true
	case telegramui.ActionKeyTab:
		return runtimehost.KeyTab, true
	case telegramui.ActionKeyCtrlC:
		return runtimehost.KeyCtrlC, true
	default:
		return "", false
	}
}

func (h *Handler) resolveInteractive(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (callbacktoken.InteractiveSession, error) {
	refs, err := h.service.CallbackSessionCandidates(actor)
	if err != nil {
		return callbacktoken.InteractiveSession{}, err
	}
	candidates := make([]callbacktoken.InteractiveSession, 0, len(refs))
	for _, ref := range refs {
		session, sessionErr := h.service.Session(actor, ref)
		if sessionErr == nil && session.InteractivePrompt != nil {
			candidates = append(candidates, callbacktoken.InteractiveSession{
				Session: ref, PromptHash: session.InteractivePrompt.Hash,
			})
		}
		if hash := h.rememberedPrompt(actor.UserID, ref); hash != "" &&
			(sessionErr != nil || session.InteractivePrompt == nil ||
				hash != session.InteractivePrompt.Hash) {
			candidates = append(candidates, callbacktoken.InteractiveSession{
				Session: ref, PromptHash: hash,
			})
		}
	}
	return h.tokens.ResolveInteractive(actor.UserID, action, token, candidates)
}

func (h *Handler) rememberPrompt(userID domain.UserID, ref domain.SessionRef, hash string) {
	h.storePromptHash(pageKey(userID, ref), hash)
}

func (h *Handler) rememberedPrompt(userID domain.UserID, ref domain.SessionRef) string {
	return h.loadPromptHash(pageKey(userID, ref))
}
