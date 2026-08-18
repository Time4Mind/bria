package telegramapp

import (
	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

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
