package telegramapp

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) handleNavigationCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
	answeredEarly bool,
) error {
	var screen telegramui.Screen
	var pageRef domain.SessionRef
	var err error
	switch callback.Action {
	case telegramui.ActionMenu:
		h.clearCreateFlow(actor.UserID)
		screen, err = h.projector.MainMenu(actor)
	case telegramui.ActionSessions:
		h.clearCreateFlow(actor.UserID)
		if callback.Token == "servers" || isLegacyNodeSessionsScreen(update.CallbackOrigin.Text) {
			h.rememberStatusReturn(actor, true)
			screen, err = h.openStatus(ctx, actor, update, telegramui.StatusChoose, true)
		} else {
			screen, err = h.openSessions(ctx, actor)
		}
	case telegramui.ActionNodesPrevious, telegramui.ActionNodesNext,
		telegramui.ActionSessionsPrevious, telegramui.ActionSessionsNext:
		screen, err = h.openListPage(actor, callback.Action, callback.Token)
	case telegramui.ActionArchive:
		screen, err = h.projector.OpenArchives(actor)
	case telegramui.ActionArchivePrevious, telegramui.ActionArchiveNext:
		screen, err = h.openArchivePage(actor, callback.Action, callback.Token)
	case telegramui.ActionSettings:
		screen, err = h.projector.Settings(actor)
	case telegramui.ActionSettingsCategory:
		screen, err = h.openSettingsCategory(actor, callback.Token)
	case telegramui.ActionOpenSetting:
		screen, err = h.openSetting(actor, callback.Token)
	case telegramui.ActionStatus:
		h.rememberStatusReturn(actor, false)
		screen, err = h.openStatus(ctx, actor, update, telegramui.StatusChoose, true)
	case telegramui.ActionStatusRefresh:
		screen, err = h.openStatus(ctx, actor, update, statusMode(callback.Token), true)
	case telegramui.ActionStatusMode:
		screen, err = h.projectStatus(actor, statusMode(callback.Token))
	case telegramui.ActionStatusLeaderNode:
		screen, err = h.confirmStatusLeader(actor, callback.Token)
	case telegramui.ActionSetLeaderNode:
		screen, err = h.confirmClusterLeader(actor, callback.Token)
	case telegramui.ActionStatusSettingsNode:
		if isLegacyNodeSessionsScreen(update.CallbackOrigin.Text) {
			screen, err = h.openLegacyNodeSettingsFromSessions(actor, callback.Token)
		} else {
			screen, err = h.openStatusNodeSettings(actor, callback.Token)
		}
	case telegramui.ActionNodeSettings:
		screen, err = h.openNodeSettingsFromSessions(actor, callback.Token)
	case telegramui.ActionNodeBackends:
		screen, err = h.openNodeBackends(actor, callback.Token)
	case telegramui.ActionNodeBackend:
		screen, err = h.openNodeBackend(actor, callback.Token)
	case telegramui.ActionNodeSpeechSetup:
		screen, err = h.setupNodeSpeech(ctx, actor, callback.Token)
	case telegramui.ActionNodeSpeechBack:
		screen, err = h.backToNodeSpeechSettings(actor, callback.Token)
	case telegramui.ActionConfirmLeader:
		screen, err = h.applyStatusLeader(ctx, actor, callback.Token)
	case telegramui.ActionSetLeaderMode:
		screen, err = h.updateLeaderMode(ctx, actor, callback.Token)
	case telegramui.ActionClusterAdd:
		screen = telegramui.RenderEnrollmentMethods(h.copy(actor))
	case telegramui.ActionClusterInvite:
		screen, err = h.createEnrollmentInvitation(ctx, actor)
	case telegramui.ActionClusterContract:
		screen = h.beginNodeContract(actor)
	case telegramui.ActionClusterUpdate, telegramui.ActionClusterUpdateYes,
		telegramui.ActionClusterUpdateRetry, telegramui.ActionClusterUpdateRefresh:
		screen, err = h.handleClusterUpdate(ctx, actor, callback.Action)
	case telegramui.ActionClusterHealth, telegramui.ActionClusterHealthRefresh:
		screen, _, err = h.openClusterHealth(actor)
	case telegramui.ActionClusterHealthAgent:
		screen, pageRef, err = h.startClusterHealthAgent(ctx, actor)
	case telegramui.ActionEnrollmentOpen:
		screen, err = h.openEnrollment(actor, telegramui.ActionEnrollmentOpen, callback.Token)
	case telegramui.ActionEnrollmentApprove, telegramui.ActionEnrollmentReject:
		screen, err = h.decideEnrollment(ctx, actor, callback.Action, callback.Token)
	case telegramui.ActionSetLanguage, telegramui.ActionSetSessionView,
		telegramui.ActionSetResumeSelection, telegramui.ActionSetIdleArchive,
		telegramui.ActionSetRetention, telegramui.ActionSetToolCalls,
		telegramui.ActionSetToolResults, telegramui.ActionSetToolOutputLines, telegramui.ActionSetThinking,
		telegramui.ActionSetResponseCards, telegramui.ActionSetNotifyFinished,
		telegramui.ActionSetTerminalSnapshots,
		telegramui.ActionSetNotifyError, telegramui.ActionSetNotifyAction,
		telegramui.ActionSetBgDismiss, telegramui.ActionSetNodeSort,
		telegramui.ActionSetQuotaPoll, telegramui.ActionSetOfflineQueue:
		screen, err = h.updateSettings(ctx, actor, callback, update.LanguageCode)
	case telegramui.ActionSetVoiceBackend:
		screen, err = h.updateVoiceSetting(ctx, actor, callback, update.LanguageCode)
	case telegramui.ActionConfirmVoiceEnable:
		screen, err = h.confirmVoiceEnable(ctx, actor, update.LanguageCode)
	case telegramui.ActionCancelVoiceEnable:
		h.takeSpeechTarget(actor.UserID)
		screen, err = h.projector.Setting(actor, telegramui.SettingVoiceBackend)
	case telegramui.ActionSelectNode:
		screen, err = h.selectNode(ctx, actor, callback.Token)
	case telegramui.ActionSelectArchiveNode:
		screen, err = h.selectArchiveNode(ctx, actor, callback.Token)
	case telegramui.ActionSelectSession:
		screen, err = h.selectSession(ctx, actor, callback.Action, callback.Token)
	case telegramui.ActionPagePrevious, telegramui.ActionPageLatest, telegramui.ActionPageNext:
		screen, pageRef, err = h.openSessionPage(
			ctx, actor, callback.Action, callback.Token,
		)
	case telegramui.ActionSelectArchive:
		screen, err = h.openArchive(ctx, actor, callback.Token)
	case telegramui.ActionArchiveBack:
		screen, err = h.backToArchive(actor, callback.Token)
	case telegramui.ActionArchiveHistory, telegramui.ActionHistoryPrevious,
		telegramui.ActionHistoryNext:
		screen, err = h.openArchiveHistory(ctx, actor, callback.Action, callback.Token)
	default:
		return nil
	}
	if callback.Action == telegramui.ActionSelectSession &&
		errors.Is(err, domain.ErrInvalidState) {
		// A card can become stale between rendering and the click (for example,
		// when reboot recovery archives a failed resume). Retrying cannot make
		// that historical selection valid and would block every later update.
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	if safeDrop(err) {
		if answeredEarly {
			return nil
		}
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	if err != nil {
		return err
	}
	if !answeredEarly {
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
	}
	var edited telegrambot.Message
	targetRich := screen.RichMarkdown || screen.Pane != nil
	replaceCarrier := !update.CallbackOrigin.Rich && targetRich
	if replaceCarrier {
		// A legacy card cannot acquire Rich Markdown by being edited through the
		// legacy endpoint: Telegram accepts the text but shows details, fences,
		// and tables literally. Replace it on the first transition regardless of
		// which action exposed the rich screen (including Sessions and paging).
	}
	pageEdit := pageRef.Validate() == nil
	if pageEdit && screen.Pane == nil && update.CallbackOrigin.Rich &&
		update.CallbackOrigin.RichMediaFileID != "" {
		// Keep the already displayed terminal image in the fast page edit. The
		// replacement worker captures a changed terminal asynchronously; reusing
		// Telegram's file ID avoids a synchronous PNG upload on every tap.
		screen.Pane = &telegramui.PaneImage{
			FileID: update.CallbackOrigin.RichMediaFileID,
			Hash:   update.CallbackOrigin.PaneHash, AnchorOffset: paneAnchorOffset(screen),
		}
	}
	viewChange := h.beginVisibleScreen(actor.UserID, screen)
	if pageEdit {
		// Stop the live worker before serializing the page edit. If it already
		// owns the edit lock, it finishes first; the user's explicit page always
		// wins and the replacement worker starts from that pinned page.
		h.cancelPaneRefresh(actor.UserID)
		h.rememberResolvedCardPage(actor.UserID, pageRef, screen)
	}
	// Every navigation that changes the response card uses the actor's lane.
	// The visible intent is published before waiting, so a slow background send
	// cannot commit over the requested screen.
	release, acquireErr := h.responseCards.acquire(ctx, actor.UserID)
	if acquireErr != nil {
		h.rollbackVisibleScreen(viewChange)
		return acquireErr
	}
	if replaceCarrier {
		// Telegram cannot safely promote a legacy carrier to Rich in place.
		// A Rich carrier can render a plain projection without being replaced;
		// retaining it keeps the keyboard that the user just tapped valid.
		edited, err = h.messenger.SendScreen(ctx, update.ChatID, screen)
		if err == nil {
			_ = h.messenger.DeleteMessage(ctx, update.CallbackOrigin)
		}
	} else {
		edited, err = h.editNavigationScreen(ctx, update.CallbackOrigin, screen)
	}
	if err == nil {
		// Persist every carrier screen, including non-session navigation. An empty
		// session checkpoint is intentional state: background workers must not
		// infer that the selected session is still visible.
		h.rememberResponseCardCoordinated(ctx, actor, edited, screen)
		if leavesSessionCard(callback.Action) {
			// A background reconciliation can start a replacement worker while a
			// slow navigation screen is being projected. Invalidate that generation
			// after the non-session checkpoint is durable and before releasing the
			// shared edit lock.
			h.cancelPaneRefresh(actor.UserID)
		}
	}
	if err != nil {
		h.rollbackVisibleScreen(viewChange)
	}
	release()
	if err == nil && (callback.Action == telegramui.ActionStatus ||
		callback.Action == telegramui.ActionStatusRefresh) {
		h.scheduleStatusRefresh(ctx, actor, edited, statusMode(callback.Token), viewChange.epoch)
	}
	if err == nil && isClusterUpdateAction(callback.Action) {
		h.scheduleClusterUpdateRefresh(ctx, actor, edited, screen)
	}
	if err == nil && pageRef.Validate() == nil {
		h.rememberResolvedCardPage(actor.UserID, pageRef, screen)
	} else if err == nil {
		h.rememberCardPage(actor.UserID, screen)
	}
	if err == nil && callback.Action == telegramui.ActionSelectSession && h.controls.pane != nil {
		if ref, resolveErr := h.resolveSession(actor, callback.Action, callback.Token); resolveErr == nil {
			h.rememberResolvedCardPage(actor.UserID, ref, screen)
			h.schedulePaneRefresh(ctx, actor, ref, edited)
		}
	}
	if err == nil && pageEdit && h.controls.pane != nil && h.sessionNeedsPaneRefresh(actor, pageRef) {
		h.schedulePaneRefresh(ctx, actor, pageRef, edited)
	}
	return err
}
