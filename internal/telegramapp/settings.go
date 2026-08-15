package telegramapp

import (
	"context"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
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
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	plans := make([]string, 0, len(nodes))
	for _, item := range nodes {
		if !item.Node.Enabled() {
			continue
		}
		plans = append(plans, speechPlan(item.Node.Name, item.Node.OS))
	}
	if len(plans) == 0 {
		plans = append(plans, h.copy(actor).Text(i18n.ToastUnavailable))
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
		h.speechMu.Lock()
		h.knownSpeechNodes[item.Node.ID] = true
		h.speechMu.Unlock()
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
	if category != telegramui.CategoryInterface && category != telegramui.CategoryCard &&
		category != telegramui.CategoryArchive && category != telegramui.CategoryNotifications &&
		category != telegramui.CategoryVoice && category != telegramui.CategoryCluster {
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

func setOfflineQueue(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "5":
		preferences.OfflineInputQueueLimit = 5
	case "10":
		preferences.OfflineInputQueueLimit = 10
	case "20":
		preferences.OfflineInputQueueLimit = 20
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setVoiceBackend(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "on":
		preferences.VoiceBackend = domain.VoiceAuto
	case "off":
		preferences.VoiceBackend = domain.VoiceOff
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setResumeSelection(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "on":
		preferences.SkipResumeSelection = false
	case "off":
		preferences.SkipResumeSelection = true
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setNodeSort(preferences *domain.UserPreferences, value string) error {
	mode := domain.NodeSortMode(value)
	if mode != domain.NodeSortCreated && mode != domain.NodeSortName && mode != domain.NodeSortLeader {
		return domain.ErrNotFound
	}
	preferences.NodeSort = mode
	return nil
}

func setQuotaPoll(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "5":
		preferences.QuotaPollMinutes = 5
	case "10":
		preferences.QuotaPollMinutes = 10
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setToolOutputLines(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "5":
		preferences.ToolOutputLines = 5
	case "10":
		preferences.ToolOutputLines = 10
	case "15":
		preferences.ToolOutputLines = 15
	case "20":
		preferences.ToolOutputLines = 20
	case "25":
		preferences.ToolOutputLines = 25
	case "30":
		preferences.ToolOutputLines = 30
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setBackgroundNotification(
	preferences *domain.UserPreferences,
	kind domain.BackgroundNoticeKind,
	value string,
) error {
	switch value {
	case "on":
		return preferences.SetBackgroundNotification(kind, true)
	case "off":
		return preferences.SetBackgroundNotification(kind, false)
	default:
		return domain.ErrNotFound
	}
}

func setBackgroundDismiss(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "1":
		preferences.BackgroundDismissSwitches = 1
	case "3":
		preferences.BackgroundDismissSwitches = 3
	case "5":
		preferences.BackgroundDismissSwitches = 5
	case "10":
		preferences.BackgroundDismissSwitches = 10
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setResponseCards(preferences *domain.UserPreferences, value string) error {
	switch domain.ResponseCardMode(value) {
	case domain.ResponseCardsKeepPaginated, domain.ResponseCardsKeepLatest,
		domain.ResponseCardsReplace:
		preferences.ResponseCards = domain.ResponseCardMode(value)
		return nil
	default:
		return domain.ErrNotFound
	}
}

func setTerminalSnapshots(preferences *domain.UserPreferences, value string) error {
	mode := domain.TerminalSnapshotMode(value)
	if mode != domain.TerminalSnapshotWorking && mode != domain.TerminalSnapshotAlways &&
		mode != domain.TerminalSnapshotNever {
		return domain.ErrNotFound
	}
	preferences.TerminalSnapshots = mode
	return nil
}

func setLanguage(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "en":
		preferences.Language = domain.LanguageEnglish
	case "ru":
		preferences.Language = domain.LanguageRussian
	case "zh":
		preferences.Language = domain.LanguageChinese
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setSessionView(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "host_first":
		preferences.SessionView = domain.ViewHostFirst
	case "all_hosts":
		preferences.SessionView = domain.ViewAllHosts
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setIdleArchive(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "6":
		preferences.IdleArchiveHours = 6
	case "12":
		preferences.IdleArchiveHours = 12
	case "24":
		preferences.IdleArchiveHours = 24
	case "unlimited":
		preferences.IdleArchiveHours = 0
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setRetention(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "14":
		preferences.ArchiveRetentionDays = 14
	case "30":
		preferences.ArchiveRetentionDays = 30
	case "unlimited":
		preferences.ArchiveRetentionDays = 0
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setExpiry(preferences *domain.UserPreferences, value string) error {
	switch value {
	case "record":
		preferences.ArchiveExpiryAction = domain.ArchiveRemoveRecord
	case "all":
		preferences.ArchiveExpiryAction = domain.ArchiveRemoveAll
	default:
		return domain.ErrNotFound
	}
	return nil
}

func setCardVisibility(
	preferences *domain.UserPreferences,
	eventType domain.CardEventType,
	value string,
) error {
	switch value {
	case "on":
		return preferences.SetCardEventVisibility(eventType, true)
	case "off":
		return preferences.SetCardEventVisibility(eventType, false)
	default:
		return domain.ErrNotFound
	}
}

func settingMutation(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionSetLanguage, telegramui.ActionSetSessionView,
		telegramui.ActionSetResumeSelection,
		telegramui.ActionSetIdleArchive, telegramui.ActionSetRetention,
		telegramui.ActionSetExpiry, telegramui.ActionSetToolCalls,
		telegramui.ActionSetToolResults, telegramui.ActionSetToolOutputLines,
		telegramui.ActionSetThinking:
		return true
	case telegramui.ActionSetResponseCards:
		return true
	case telegramui.ActionSetTerminalSnapshots:
		return true
	case telegramui.ActionSetNotifyFinished, telegramui.ActionSetNotifyError,
		telegramui.ActionSetNotifyAction, telegramui.ActionSetBgDismiss:
		return true
	case telegramui.ActionSetNodeSort, telegramui.ActionSetQuotaPoll,
		telegramui.ActionSetLeaderMode, telegramui.ActionSetOfflineQueue:
		return true
	case telegramui.ActionSetVoiceBackend, telegramui.ActionConfirmVoiceEnable,
		telegramui.ActionCancelVoiceEnable:
		return true
	default:
		return false
	}
}

func (h *Handler) copy(actor application.Principal) i18n.Localizer {
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return i18n.For("en")
	}
	return i18n.For(string(preferences.EffectiveLanguage()))
}
