package telegramui

import "github.com/Time4Mind/bria/internal/i18n"

func settingChoices(input SettingsInput, id SettingID) Grid {
	copy := input.Copy
	switch id {
	case SettingSessionView:
		return choiceRows(
			button(selectedLabel(!input.AllHosts, copy.Text(i18n.ValueHostFirst)), ActionSetSessionView, "host_first"),
			button(selectedLabel(input.AllHosts, copy.Text(i18n.ValueAllHosts)), ActionSetSessionView, "all_hosts"),
		)
	case SettingResumeSelection:
		return visibilityChoices(copy, input.ResumeSelection, ActionSetResumeSelection)
	case SettingLanguage:
		return choiceRows(
			button(selectedLabel(copy.Language() == "en", copy.Text(i18n.LanguageEnglish)), ActionSetLanguage, "en"),
			button(selectedLabel(copy.Language() == "ru", copy.Text(i18n.LanguageRussian)), ActionSetLanguage, "ru"),
			button(selectedLabel(copy.Language() == "zh", copy.Text(i18n.LanguageChinese)), ActionSetLanguage, "zh"),
		)
	case SettingToolCalls:
		return visibilityChoices(copy, input.ShowToolCalls, ActionSetToolCalls)
	case SettingToolResults:
		return visibilityChoices(copy, input.ShowToolResults, ActionSetToolResults)
	case SettingToolOutputLines:
		return choiceRows(
			button(selectedLabel(input.ToolOutputLines == 5, "5"), ActionSetToolOutputLines, "5"),
			button(selectedLabel(input.ToolOutputLines == 10, "10"), ActionSetToolOutputLines, "10"),
			button(selectedLabel(input.ToolOutputLines == 15, "15"), ActionSetToolOutputLines, "15"),
			button(selectedLabel(input.ToolOutputLines == 20, "20"), ActionSetToolOutputLines, "20"),
			button(selectedLabel(input.ToolOutputLines == 25, "25"), ActionSetToolOutputLines, "25"),
			button(selectedLabel(input.ToolOutputLines == 30, "30"), ActionSetToolOutputLines, "30"),
		)
	case SettingThinking:
		return visibilityChoices(copy, input.ShowThinking, ActionSetThinking)
	case SettingResponseCards:
		return choiceRows(
			button(selectedLabel(input.ResponseCards == "keep_paginated", copy.Text(i18n.ValueCardsKeepPaginated)), ActionSetResponseCards, "keep_paginated"),
			button(selectedLabel(input.ResponseCards == "keep_latest", copy.Text(i18n.ValueCardsKeepLatest)), ActionSetResponseCards, "keep_latest"),
			button(selectedLabel(input.ResponseCards == "replace_paginated", copy.Text(i18n.ValueCardsReplace)), ActionSetResponseCards, "replace_paginated"),
		)
	case SettingTerminalSnapshots:
		return choiceRows(
			button(selectedLabel(input.TerminalSnapshots == "working", copy.Text(i18n.ValueTerminalWorking)), ActionSetTerminalSnapshots, "working"),
			button(selectedLabel(input.TerminalSnapshots == "always", copy.Text(i18n.ValueTerminalAlways)), ActionSetTerminalSnapshots, "always"),
			button(selectedLabel(input.TerminalSnapshots == "never", copy.Text(i18n.ValueTerminalNever)), ActionSetTerminalSnapshots, "never"),
		)
	case SettingIdleArchive:
		return choiceRows(
			button(selectedLabel(input.IdleHours == 6, copy.Count(i18n.CountHour, 6)), ActionSetIdleArchive, "6"),
			button(selectedLabel(input.IdleHours == 12, copy.Count(i18n.CountHour, 12)), ActionSetIdleArchive, "12"),
			button(selectedLabel(input.IdleHours == 24, copy.Count(i18n.CountHour, 24)), ActionSetIdleArchive, "24"),
			button(selectedLabel(input.IdleHours == 0, copy.Text(i18n.ValueUnlimited)), ActionSetIdleArchive, "unlimited"),
		)
	case SettingRetention:
		return choiceRows(
			button(selectedLabel(input.RetentionDays == 14, copy.Count(i18n.CountDay, 14)), ActionSetRetention, "14"),
			button(selectedLabel(input.RetentionDays == 30, copy.Count(i18n.CountDay, 30)), ActionSetRetention, "30"),
			button(selectedLabel(input.RetentionDays == 0, copy.Text(i18n.ValueUnlimited)), ActionSetRetention, "unlimited"),
		)
	case SettingExpiry:
		return choiceRows(
			button(selectedLabel(!input.RemoveAllOnPurge, copy.Text(i18n.ValueRecordOnly)), ActionSetExpiry, "record"),
			button(selectedLabel(input.RemoveAllOnPurge, copy.Text(i18n.ValueDeleteFiles)), ActionSetExpiry, "all"),
		)
	case SettingNotifyFinished:
		return visibilityChoices(copy, input.NotifyFinished, ActionSetNotifyFinished)
	case SettingNotifyError:
		return visibilityChoices(copy, input.NotifyError, ActionSetNotifyError)
	case SettingNotifyAction:
		return visibilityChoices(copy, input.NotifyAction, ActionSetNotifyAction)
	case SettingBackgroundDismiss:
		return choiceRows(
			button(selectedLabel(input.BackgroundDismiss == 1, "1"), ActionSetBgDismiss, "1"),
			button(selectedLabel(input.BackgroundDismiss == 3, "3"), ActionSetBgDismiss, "3"),
			button(selectedLabel(input.BackgroundDismiss == 5, "5"), ActionSetBgDismiss, "5"),
			button(selectedLabel(input.BackgroundDismiss == 10, "10"), ActionSetBgDismiss, "10"),
		)
	case SettingNodeSort:
		return choiceRows(
			button(selectedLabel(input.NodeSort == "created", copy.Text(i18n.ValueNodeCreated)), ActionSetNodeSort, "created"),
			button(selectedLabel(input.NodeSort == "name", copy.Text(i18n.ValueNodeName)), ActionSetNodeSort, "name"),
			button(selectedLabel(input.NodeSort == "leader", copy.Text(i18n.ValueNodeLeader)), ActionSetNodeSort, "leader"),
		)
	case SettingQuotaPoll:
		return choiceRows(
			button(selectedLabel(input.QuotaPollMinutes == 5, copy.Format(i18n.ValueMinuteShort, 5)), ActionSetQuotaPoll, "5"),
			button(selectedLabel(input.QuotaPollMinutes == 10, copy.Format(i18n.ValueMinuteShort, 10)), ActionSetQuotaPoll, "10"),
		)
	case SettingLeaderMode:
		return choiceRows(
			button(selectedLabel(!input.LeaderAutomatic, copy.Text(i18n.ValueLeaderManual)), ActionSetLeaderMode, "manual"),
			button(selectedLabel(input.LeaderAutomatic, copy.Text(i18n.ValueLeaderAutomatic)), ActionSetLeaderMode, "automatic"),
		)
	case SettingLeaderNode:
		rows := make(Grid, 0, len(input.LeaderNodes))
		for _, node := range input.LeaderNodes {
			label := selectedLabel(node.Selected, node.Name)
			action, token := ActionSetLeaderNode, node.Token
			if node.Disabled {
				action, token = ActionNoop, ""
			}
			rows = append(rows, Row{button(label, action, token)})
		}
		return rows
	case SettingVoiceBackend:
		return visibilityChoices(copy, input.VoiceBackend != "off", ActionSetVoiceBackend)
	case SettingOfflineQueue:
		return choiceRows(
			button(selectedLabel(input.OfflineQueueLimit == 5, "5"), ActionSetOfflineQueue, "5"),
			button(selectedLabel(input.OfflineQueueLimit == 10, "10"), ActionSetOfflineQueue, "10"),
			button(selectedLabel(input.OfflineQueueLimit == 20, "20"), ActionSetOfflineQueue, "20"),
		)
	default:
		return nil
	}
}
