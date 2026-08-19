package telegramapp

import (
	"context"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/speechsetup"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) updateVoiceSetting(
	ctx context.Context,
	actor application.Principal,
	callback telegramui.Callback,
	languageCode string,
) (telegramui.Screen, error) {
	if callback.Token == "off" {
		return h.updateSettings(ctx, actor, callback, languageCode)
	}
	if callback.Token != "on" {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	h.speechMu.Lock()
	delete(h.speechTargets, actor.UserID)
	h.speechMu.Unlock()
	plans, err := h.voiceEnablePlans(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderVoiceEnableConfirmation(h.copy(actor), plans), nil
}

func (h *Handler) confirmVoiceEnable(
	ctx context.Context,
	actor application.Principal,
	languageCode string,
) (telegramui.Screen, error) {
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if preferences.Language == domain.LanguageAuto {
		preferences.Language = domain.LanguageFromTelegram(languageCode)
	}
	preferences.VoiceBackend = domain.VoiceAuto
	if err := h.service.SetPreferences(ctx, actor, preferences); err != nil {
		return telegramui.Screen{}, err
	}
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	lines := make([]string, 0, len(nodes))
	target := h.takeSpeechTarget(actor.UserID)
	for _, item := range nodes {
		if target != "" && item.Node.ID != target {
			continue
		}
		if !item.Node.Enabled() || item.Node.Status == domain.NodeOffline {
			lines = append(lines, item.Node.Name+": postponed (offline)")
			continue
		}
		if h.speechSetup == nil {
			lines = append(lines, item.Node.Name+": setup service unavailable")
			continue
		}
		status, startErr := h.speechSetup.Start(ctx, speechsetup.Request{NodeID: string(item.Node.ID)})
		if startErr != nil {
			lines = append(lines, item.Node.Name+": postponed ("+shortSetupError(startErr)+")")
			continue
		}
		if status.Phase == speechsetup.PhaseReady {
			h.markSpeechNodeKnown(item.Node.ID)
		}
		lines = append(lines, item.Node.Name+": "+speechStatusText(status))
	}
	if target != "" {
		back, tokenErr := h.tokens.Node(actor.UserID, telegramui.ActionNodeSpeechBack, target)
		if tokenErr != nil {
			return telegramui.Screen{}, tokenErr
		}
		return telegramui.RenderNodeVoiceSetupStarted(h.copy(actor), lines, back), nil
	}
	return telegramui.RenderVoiceSetupStarted(h.copy(actor), lines), nil
}

func speechPlan(name, operatingSystem string) string {
	if strings.EqualFold(operatingSystem, "darwin") {
		return name + ": Apple Speech; macOS will request Speech Recognition permission"
	}
	return name + ": local Whisper"
}

func speechStatusText(status speechsetup.Status) string {
	switch status.Phase {
	case speechsetup.PhaseReady:
		return status.Engine + " ready"
	case speechsetup.PhasePermissionRequired:
		return "confirm Speech Recognition permission in macOS"
	case speechsetup.PhaseInstalling:
		return status.Engine + " installation started"
	default:
		if status.Detail != "" {
			return status.Detail
		}
		return string(status.Phase)
	}
}

func shortSetupError(err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 100 {
		return text[:100] + "…"
	}
	return text
}

func (h *Handler) setupNodeSpeech(
	ctx context.Context, actor application.Principal, token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionNodeSpeechSetup, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if h.speechSetup == nil {
		return telegramui.Screen{}, domain.ErrInvalidState
	}
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if preferences.EffectiveVoiceBackend() == domain.VoiceOff {
		h.speechMu.Lock()
		h.speechTargets[actor.UserID] = nodeID
		h.speechMu.Unlock()
		return telegramui.RenderVoiceEnableConfirmation(h.copy(actor), []string{
			speechPlan(string(nodeID), ""),
		}), nil
	}
	status, err := h.speechSetup.Start(ctx, speechsetup.Request{NodeID: string(nodeID)})
	if err != nil {
		return telegramui.Screen{}, err
	}
	back, err := h.tokens.Node(actor.UserID, telegramui.ActionNodeSpeechBack, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderNodeVoiceSetupStarted(h.copy(actor), []string{
		string(nodeID) + ": " + speechStatusText(status),
	}, back), nil
}

func (h *Handler) backToNodeSpeechSettings(
	actor application.Principal, token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionNodeSpeechBack, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return h.projectNodeSettings(actor, nodeID)
}

func (h *Handler) takeSpeechTarget(userID domain.UserID) domain.NodeID {
	h.speechMu.Lock()
	defer h.speechMu.Unlock()
	target := h.speechTargets[userID]
	delete(h.speechTargets, userID)
	return target
}

func (h *Handler) ensureLanguage(
	ctx context.Context,
	actor application.Principal,
	languageCode string,
) error {
	preferences, err := h.service.Preferences(actor)
	if err != nil || preferences.Language != domain.LanguageAuto {
		return err
	}
	preferences.Language = domain.LanguageFromTelegram(languageCode)
	return h.service.SetPreferences(ctx, actor, preferences)
}

func (h *Handler) openSettingsCategory(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	category := telegramui.SettingsCategory(token)
	// Old keyboards may still contain the former standalone voice category.
	// Keep those callbacks useful after speech settings were folded into Interface.
	if category == telegramui.CategoryVoice {
		category = telegramui.CategoryInterface
	}
	if category != telegramui.CategoryInterface && category != telegramui.CategoryCard &&
		category != telegramui.CategoryArchive && category != telegramui.CategoryNotifications &&
		category != telegramui.CategoryCluster {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	return h.projector.SettingsCategory(actor, category)
}

func (h *Handler) openSetting(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	setting := telegramui.SettingID(token)
	switch setting {
	case telegramui.SettingSessionView, telegramui.SettingResumeSelection,
		telegramui.SettingLanguage,
		telegramui.SettingToolCalls, telegramui.SettingToolResults, telegramui.SettingThinking, telegramui.SettingToolOutputLines,
		telegramui.SettingResponseCards,
		telegramui.SettingTerminalSnapshots,
		telegramui.SettingIdleArchive, telegramui.SettingRetention, telegramui.SettingExpiry,
		telegramui.SettingNotifyFinished, telegramui.SettingNotifyError,
		telegramui.SettingNotifyAction, telegramui.SettingBackgroundDismiss,
		telegramui.SettingNodeSort, telegramui.SettingQuotaPoll, telegramui.SettingLeaderMode,
		telegramui.SettingLeaderNode,
		telegramui.SettingVoiceBackend, telegramui.SettingOfflineQueue:
		return h.projector.Setting(actor, setting)
	default:
		return telegramui.Screen{}, domain.ErrNotFound
	}
}

func (h *Handler) updateSettings(
	ctx context.Context,
	actor application.Principal,
	callback telegramui.Callback,
	languageCode string,
) (telegramui.Screen, error) {
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if preferences.Language == domain.LanguageAuto {
		preferences.Language = domain.LanguageFromTelegram(languageCode)
	}
	setting, err := applySettingChoice(&preferences, callback)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if err := h.service.SetPreferences(ctx, actor, preferences); err != nil {
		return telegramui.Screen{}, err
	}
	return h.projector.Setting(actor, setting)
}

func applySettingChoice(
	preferences *domain.UserPreferences,
	callback telegramui.Callback,
) (telegramui.SettingID, error) {
	value := string(callback.Token)
	switch callback.Action {
	case telegramui.ActionSetLanguage:
		return telegramui.SettingLanguage, setLanguage(preferences, value)
	case telegramui.ActionSetSessionView:
		return telegramui.SettingSessionView, setSessionView(preferences, value)
	case telegramui.ActionSetResumeSelection:
		return telegramui.SettingResumeSelection, setResumeSelection(preferences, value)
	case telegramui.ActionSetIdleArchive:
		return telegramui.SettingIdleArchive, setIdleArchive(preferences, value)
	case telegramui.ActionSetRetention:
		return telegramui.SettingRetention, setRetention(preferences, value)
	case telegramui.ActionSetExpiry:
		return telegramui.SettingExpiry, setExpiry(preferences, value)
	case telegramui.ActionSetToolCalls:
		return telegramui.SettingToolCalls, setCardVisibility(preferences, domain.CardEventToolCall, value)
	case telegramui.ActionSetToolResults:
		return telegramui.SettingToolResults, setCardVisibility(preferences, domain.CardEventToolResult, value)
	case telegramui.ActionSetToolOutputLines:
		return telegramui.SettingToolOutputLines, setToolOutputLines(preferences, value)
	case telegramui.ActionSetThinking:
		return telegramui.SettingThinking, setCardVisibility(preferences, domain.CardEventThinking, value)
	case telegramui.ActionSetResponseCards:
		return telegramui.SettingResponseCards, setResponseCards(preferences, value)
	case telegramui.ActionSetTerminalSnapshots:
		return telegramui.SettingTerminalSnapshots, setTerminalSnapshots(preferences, value)
	case telegramui.ActionSetNotifyFinished:
		return telegramui.SettingNotifyFinished,
			setBackgroundNotification(preferences, domain.BackgroundFinished, value)
	case telegramui.ActionSetNotifyError:
		return telegramui.SettingNotifyError,
			setBackgroundNotification(preferences, domain.BackgroundError, value)
	case telegramui.ActionSetNotifyAction:
		return telegramui.SettingNotifyAction,
			setBackgroundNotification(preferences, domain.BackgroundNeedsAction, value)
	case telegramui.ActionSetBgDismiss:
		return telegramui.SettingBackgroundDismiss, setBackgroundDismiss(preferences, value)
	case telegramui.ActionSetNodeSort:
		return telegramui.SettingNodeSort, setNodeSort(preferences, value)
	case telegramui.ActionSetQuotaPoll:
		return telegramui.SettingQuotaPoll, setQuotaPoll(preferences, value)
	case telegramui.ActionSetVoiceBackend:
		return telegramui.SettingVoiceBackend, setVoiceBackend(preferences, value)
	case telegramui.ActionSetOfflineQueue:
		return telegramui.SettingOfflineQueue, setOfflineQueue(preferences, value)
	default:
		return "", domain.ErrNotFound
	}
}
