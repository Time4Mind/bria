package telegramui

import "github.com/Time4Mind/bria/internal/i18n"

type SettingsCategory string
type SettingID string

const (
	CategoryInterface     SettingsCategory = "interface"
	CategoryCard          SettingsCategory = "card"
	CategoryArchive       SettingsCategory = "archive"
	CategoryNotifications SettingsCategory = "notifications"
	CategoryVoice         SettingsCategory = "voice"
	CategoryCluster       SettingsCategory = "cluster"

	SettingSessionView       SettingID = "session_view"
	SettingResumeSelection   SettingID = "resume_selection"
	SettingLanguage          SettingID = "language"
	SettingToolCalls         SettingID = "show_tool_calls"
	SettingToolResults       SettingID = "show_tool_results"
	SettingToolOutputLines   SettingID = "tool_output_lines"
	SettingThinking          SettingID = "show_thinking"
	SettingResponseCards     SettingID = "response_cards"
	SettingTerminalSnapshots SettingID = "terminal_snapshots"
	SettingIdleArchive       SettingID = "idle_archive"
	SettingRetention         SettingID = "retention"
	SettingExpiry            SettingID = "expiry"
	SettingNotifyFinished    SettingID = "notify_finished"
	SettingNotifyError       SettingID = "notify_error"
	SettingNotifyAction      SettingID = "notify_action"
	SettingBackgroundDismiss SettingID = "background_dismiss"
	SettingNodeSort          SettingID = "node_sort"
	SettingQuotaPoll         SettingID = "quota_poll"
	SettingVoiceBackend      SettingID = "voice_backend"
	SettingOfflineQueue      SettingID = "offline_queue"
)

type settingDescriptor struct {
	id       SettingID
	category SettingsCategory
	label    i18n.Key
	body     i18n.Key
}

var settingsCatalog = []settingDescriptor{
	{id: SettingSessionView, category: CategoryInterface, label: i18n.SettingSessionView, body: i18n.SettingSessionViewBody},
	{id: SettingResumeSelection, category: CategoryInterface, label: i18n.SettingResumeSelection, body: i18n.SettingResumeSelectionBody},
	{id: SettingLanguage, category: CategoryInterface, label: i18n.SettingLanguage, body: i18n.SettingLanguageBody},
	{id: SettingOfflineQueue, category: CategoryInterface, label: i18n.SettingOfflineQueue, body: i18n.SettingOfflineQueueBody},
	{id: SettingToolCalls, category: CategoryCard, label: i18n.SettingToolCalls, body: i18n.SettingToolCallsBody},
	{id: SettingToolResults, category: CategoryCard, label: i18n.SettingToolResults, body: i18n.SettingToolResultsBody},
	{id: SettingToolOutputLines, category: CategoryCard, label: i18n.SettingToolOutputLines, body: i18n.SettingToolOutputLinesBody},
	{id: SettingThinking, category: CategoryCard, label: i18n.SettingThinking, body: i18n.SettingThinkingBody},
	{id: SettingResponseCards, category: CategoryCard, label: i18n.SettingResponseCards, body: i18n.SettingResponseCardsBody},
	{id: SettingTerminalSnapshots, category: CategoryCard, label: i18n.SettingTerminalSnapshots, body: i18n.SettingTerminalSnapshotsBody},
	{id: SettingIdleArchive, category: CategoryArchive, label: i18n.SettingIdleArchive, body: i18n.SettingIdleArchiveBody},
	{id: SettingRetention, category: CategoryArchive, label: i18n.SettingRetention, body: i18n.SettingRetentionBody},
	{id: SettingExpiry, category: CategoryArchive, label: i18n.SettingExpiry, body: i18n.SettingExpiryBody},
	{id: SettingNotifyFinished, category: CategoryNotifications, label: i18n.SettingNotifyFinished, body: i18n.SettingNotifyFinishedBody},
	{id: SettingNotifyError, category: CategoryNotifications, label: i18n.SettingNotifyError, body: i18n.SettingNotifyErrorBody},
	{id: SettingNotifyAction, category: CategoryNotifications, label: i18n.SettingNotifyAction, body: i18n.SettingNotifyActionBody},
	{id: SettingBackgroundDismiss, category: CategoryNotifications, label: i18n.SettingBackgroundDismiss, body: i18n.SettingBackgroundDismissBody},
	{id: SettingVoiceBackend, category: CategoryVoice, label: i18n.SettingVoiceBackend, body: i18n.SettingVoiceBackendBody},
	{id: SettingNodeSort, category: CategoryCluster, label: i18n.SettingNodeSort, body: i18n.SettingNodeSortBody},
	{id: SettingQuotaPoll, category: CategoryCluster, label: i18n.SettingQuotaPoll, body: i18n.SettingQuotaPollBody},
}

func descriptorFor(id SettingID) (settingDescriptor, bool) {
	for _, descriptor := range settingsCatalog {
		if descriptor.id == id {
			return descriptor, true
		}
	}
	return settingDescriptor{}, false
}

func settingsIn(category SettingsCategory) []settingDescriptor {
	result := make([]settingDescriptor, 0)
	for _, descriptor := range settingsCatalog {
		if descriptor.category == category {
			result = append(result, descriptor)
		}
	}
	return result
}

func validSettingsCategory(category SettingsCategory) bool {
	return category == CategoryInterface || category == CategoryCard ||
		category == CategoryArchive || category == CategoryNotifications ||
		category == CategoryVoice || category == CategoryCluster
}
