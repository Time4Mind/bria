package telegramui

import (
	"fmt"
	"html"
	"strings"

	"github.com/Time4Mind/bria/internal/i18n"
)

type SettingsInput struct {
	Copy               i18n.Localizer
	AllHosts           bool
	ResumeSelection    bool
	ShowToolCalls      bool
	ShowToolResults    bool
	ToolOutputLines    int
	ShowThinking       bool
	ResponseCards      string
	TerminalSnapshots  string
	IdleHours          int
	RetentionDays      int
	RemoveAllOnPurge   bool
	NotifyFinished     bool
	NotifyError        bool
	NotifyAction       bool
	BackgroundDismiss  int
	NodeSort           string
	QuotaPollMinutes   int
	VoiceBackend       string
	OfflineQueueLimit  int
	ClusterAccounts    string
	PendingEnrollments []PendingEnrollmentItem
}

func RenderVoiceEnableConfirmation(copy i18n.Localizer, plans []string) Screen {
	plan := make([]string, 0, len(plans))
	for _, item := range plans {
		plan = append(plan, "• "+html.EscapeString(item))
	}
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: copy.Format(i18n.VoiceEnableConfirm, strings.Join(plan, "\n")),
		Grid: Grid{
			Row{button(copy.Text(i18n.ButtonConfirm), ActionConfirmVoiceEnable, "")},
			Row{button(copy.Text(i18n.ButtonCancel), ActionCancelVoiceEnable, "")},
		},
	}
}

func RenderVoiceSetupStarted(copy i18n.Localizer, lines []string) Screen {
	items := make([]string, 0, len(lines))
	for _, line := range lines {
		items = append(items, "• "+html.EscapeString(line))
	}
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: copy.Format(i18n.VoiceEnablePlan, strings.Join(items, "\n")),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonBack), ActionOpenSetting,
			OpaqueToken(SettingVoiceBackend))}},
	}
}

func RenderNewNodeVoiceSetup(copy i18n.Localizer, name string, token OpaqueToken) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: copy.Format(i18n.VoiceEnablePlan, "• "+html.EscapeString(name)+
			": "+html.EscapeString(copy.Text(i18n.VoiceSetupRequired))),
		Grid: Grid{Row{
			button(copy.Text(i18n.NodeSpeechSetup), ActionNodeSpeechSetup, token),
			button(copy.Text(i18n.ButtonLater), ActionCancelVoiceEnable, ""),
		}},
	}
}

func RenderNodeVoiceSetupStarted(
	copy i18n.Localizer, lines []string, back OpaqueToken,
) Screen {
	screen := RenderVoiceSetupStarted(copy, lines)
	screen.Grid = Grid{Row{button(copy.Text(i18n.ButtonBack), ActionNodeSpeechBack, back)}}
	return screen
}

func RenderSettings(input SettingsInput) Screen {
	copy := input.Copy
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.SettingsTitle) + "</b>\n\n" + copy.Text(i18n.SettingsBody),
		Grid: Grid{
			Row{button(copy.Text(i18n.SettingsInterface), ActionSettingsCategory, OpaqueToken(CategoryInterface))},
			Row{button(copy.Text(i18n.SettingsCard), ActionSettingsCategory, OpaqueToken(CategoryCard))},
			Row{button(copy.Text(i18n.SettingsArchive), ActionSettingsCategory, OpaqueToken(CategoryArchive))},
			Row{button(copy.Text(i18n.SettingsNotifications), ActionSettingsCategory, OpaqueToken(CategoryNotifications))},
			Row{button(copy.Text(i18n.SettingsVoice), ActionSettingsCategory, OpaqueToken(CategoryVoice))},
			Row{button(copy.Text(i18n.SettingsCluster), ActionSettingsCategory, OpaqueToken(CategoryCluster))},
			Row{button(copy.Text(i18n.ButtonBack), ActionMenu, "")},
		},
	}
}

func RenderSettingsCategory(input SettingsInput, category SettingsCategory) (Screen, error) {
	if !validSettingsCategory(category) {
		return Screen{}, fmt.Errorf("unknown settings category: %q", category)
	}
	copy := input.Copy
	rows := make(Grid, 0, len(settingsIn(category))+1)
	if category == CategoryCluster {
		rows = append(rows, clusterSettingsActions(input)...)
	}
	for _, descriptor := range settingsIn(category) {
		label := copy.Text(descriptor.label) + ": " + settingValue(input, descriptor.id)
		rows = append(rows, Row{button(label, ActionOpenSetting, OpaqueToken(descriptor.id))})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSettings, "")})
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: settingsCategoryText(input, category),
		Grid: rows,
	}, nil
}

func RenderSetting(input SettingsInput, id SettingID) (Screen, error) {
	descriptor, ok := descriptorFor(id)
	if !ok {
		return Screen{}, fmt.Errorf("unknown setting: %q", id)
	}
	copy := input.Copy
	rows := settingChoices(input, id)
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory,
		OpaqueToken(descriptor.category))})
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(descriptor.label) + "</b>\n\n" + copy.Text(descriptor.body),
		Grid: rows,
	}, nil
}

func settingChoices(input SettingsInput, id SettingID) Grid {
	copy := input.Copy
	switch id {
	case SettingSessionView:
		return Grid{Row{
			button(selectedLabel(!input.AllHosts, copy.Text(i18n.ValueHostFirst)), ActionSetSessionView, "host_first"),
			button(selectedLabel(input.AllHosts, copy.Text(i18n.ValueAllHosts)), ActionSetSessionView, "all_hosts"),
		}}
	case SettingResumeSelection:
		return visibilityChoices(copy, input.ResumeSelection, ActionSetResumeSelection)
	case SettingLanguage:
		return Grid{Row{
			button(selectedLabel(copy.Language() == "en", copy.Text(i18n.LanguageEnglish)), ActionSetLanguage, "en"),
			button(selectedLabel(copy.Language() == "ru", copy.Text(i18n.LanguageRussian)), ActionSetLanguage, "ru"),
			button(selectedLabel(copy.Language() == "zh", copy.Text(i18n.LanguageChinese)), ActionSetLanguage, "zh"),
		}}
	case SettingToolCalls:
		return visibilityChoices(copy, input.ShowToolCalls, ActionSetToolCalls)
	case SettingToolResults:
		return visibilityChoices(copy, input.ShowToolResults, ActionSetToolResults)
	case SettingToolOutputLines:
		return Grid{
			Row{
				button(selectedLabel(input.ToolOutputLines == 5, "5"), ActionSetToolOutputLines, "5"),
				button(selectedLabel(input.ToolOutputLines == 10, "10"), ActionSetToolOutputLines, "10"),
				button(selectedLabel(input.ToolOutputLines == 15, "15"), ActionSetToolOutputLines, "15"),
			},
			Row{
				button(selectedLabel(input.ToolOutputLines == 20, "20"), ActionSetToolOutputLines, "20"),
				button(selectedLabel(input.ToolOutputLines == 25, "25"), ActionSetToolOutputLines, "25"),
				button(selectedLabel(input.ToolOutputLines == 30, "30"), ActionSetToolOutputLines, "30"),
			},
		}
	case SettingThinking:
		return visibilityChoices(copy, input.ShowThinking, ActionSetThinking)
	case SettingResponseCards:
		return Grid{
			Row{button(selectedLabel(input.ResponseCards == "keep_paginated", copy.Text(i18n.ValueCardsKeepPaginated)), ActionSetResponseCards, "keep_paginated")},
			Row{button(selectedLabel(input.ResponseCards == "keep_latest", copy.Text(i18n.ValueCardsKeepLatest)), ActionSetResponseCards, "keep_latest")},
			Row{button(selectedLabel(input.ResponseCards == "replace_paginated", copy.Text(i18n.ValueCardsReplace)), ActionSetResponseCards, "replace_paginated")},
		}
	case SettingTerminalSnapshots:
		return Grid{
			Row{button(selectedLabel(input.TerminalSnapshots == "working", copy.Text(i18n.ValueTerminalWorking)), ActionSetTerminalSnapshots, "working")},
			Row{button(selectedLabel(input.TerminalSnapshots == "always", copy.Text(i18n.ValueTerminalAlways)), ActionSetTerminalSnapshots, "always")},
			Row{button(selectedLabel(input.TerminalSnapshots == "never", copy.Text(i18n.ValueTerminalNever)), ActionSetTerminalSnapshots, "never")},
		}
	case SettingIdleArchive:
		return Grid{
			Row{
				button(selectedLabel(input.IdleHours == 6, copy.Count(i18n.CountHour, 6)), ActionSetIdleArchive, "6"),
				button(selectedLabel(input.IdleHours == 12, copy.Count(i18n.CountHour, 12)), ActionSetIdleArchive, "12"),
			},
			Row{
				button(selectedLabel(input.IdleHours == 24, copy.Count(i18n.CountHour, 24)), ActionSetIdleArchive, "24"),
				button(selectedLabel(input.IdleHours == 0, copy.Text(i18n.ValueUnlimited)), ActionSetIdleArchive, "unlimited"),
			},
		}
	case SettingRetention:
		return Grid{Row{
			button(selectedLabel(input.RetentionDays == 14, copy.Count(i18n.CountDay, 14)), ActionSetRetention, "14"),
			button(selectedLabel(input.RetentionDays == 30, copy.Count(i18n.CountDay, 30)), ActionSetRetention, "30"),
			button(selectedLabel(input.RetentionDays == 0, copy.Text(i18n.ValueUnlimited)), ActionSetRetention, "unlimited"),
		}}
	case SettingExpiry:
		return Grid{Row{
			button(selectedLabel(!input.RemoveAllOnPurge, copy.Text(i18n.ValueRecordOnly)), ActionSetExpiry, "record"),
			button(selectedLabel(input.RemoveAllOnPurge, copy.Text(i18n.ValueDeleteFiles)), ActionSetExpiry, "all"),
		}}
	case SettingNotifyFinished:
		return visibilityChoices(copy, input.NotifyFinished, ActionSetNotifyFinished)
	case SettingNotifyError:
		return visibilityChoices(copy, input.NotifyError, ActionSetNotifyError)
	case SettingNotifyAction:
		return visibilityChoices(copy, input.NotifyAction, ActionSetNotifyAction)
	case SettingBackgroundDismiss:
		return Grid{Row{
			button(selectedLabel(input.BackgroundDismiss == 1, "1"), ActionSetBgDismiss, "1"),
			button(selectedLabel(input.BackgroundDismiss == 3, "3"), ActionSetBgDismiss, "3"),
			button(selectedLabel(input.BackgroundDismiss == 5, "5"), ActionSetBgDismiss, "5"),
			button(selectedLabel(input.BackgroundDismiss == 10, "10"), ActionSetBgDismiss, "10"),
		}}
	case SettingNodeSort:
		return Grid{Row{
			button(selectedLabel(input.NodeSort == "created", copy.Text(i18n.ValueNodeCreated)), ActionSetNodeSort, "created"),
			button(selectedLabel(input.NodeSort == "name", copy.Text(i18n.ValueNodeName)), ActionSetNodeSort, "name"),
			button(selectedLabel(input.NodeSort == "leader", copy.Text(i18n.ValueNodeLeader)), ActionSetNodeSort, "leader"),
		}}
	case SettingQuotaPoll:
		return Grid{Row{
			button(selectedLabel(input.QuotaPollMinutes == 5, copy.Format(i18n.ValueMinuteShort, 5)), ActionSetQuotaPoll, "5"),
			button(selectedLabel(input.QuotaPollMinutes == 10, copy.Format(i18n.ValueMinuteShort, 10)), ActionSetQuotaPoll, "10"),
		}}
	case SettingVoiceBackend:
		return visibilityChoices(copy, input.VoiceBackend != "off", ActionSetVoiceBackend)
	case SettingOfflineQueue:
		return Grid{Row{
			button(selectedLabel(input.OfflineQueueLimit == 5, "5"), ActionSetOfflineQueue, "5"),
			button(selectedLabel(input.OfflineQueueLimit == 10, "10"), ActionSetOfflineQueue, "10"),
			button(selectedLabel(input.OfflineQueueLimit == 20, "20"), ActionSetOfflineQueue, "20"),
		}}
	default:
		return nil
	}
}

func settingValue(input SettingsInput, id SettingID) string {
	copy := input.Copy
	switch id {
	case SettingSessionView:
		return selectedValue(input.AllHosts, copy.Text(i18n.ValueAllHosts), copy.Text(i18n.ValueHostFirst))
	case SettingResumeSelection:
		return visibilityValue(copy, input.ResumeSelection)
	case SettingLanguage:
		switch copy.Language() {
		case "ru":
			return copy.Text(i18n.LanguageRussian)
		case "zh":
			return copy.Text(i18n.LanguageChinese)
		default:
			return copy.Text(i18n.LanguageEnglish)
		}
	case SettingToolCalls:
		return visibilityValue(copy, input.ShowToolCalls)
	case SettingToolResults:
		return visibilityValue(copy, input.ShowToolResults)
	case SettingToolOutputLines:
		return copy.Format(i18n.ValueLines, input.ToolOutputLines)
	case SettingThinking:
		return visibilityValue(copy, input.ShowThinking)
	case SettingResponseCards:
		switch input.ResponseCards {
		case "keep_latest":
			return copy.Text(i18n.ValueCardsKeepLatest)
		case "replace_paginated":
			return copy.Text(i18n.ValueCardsReplace)
		default:
			return copy.Text(i18n.ValueCardsKeepPaginated)
		}
	case SettingTerminalSnapshots:
		switch input.TerminalSnapshots {
		case "always":
			return copy.Text(i18n.ValueTerminalAlways)
		case "never":
			return copy.Text(i18n.ValueTerminalNever)
		default:
			return copy.Text(i18n.ValueTerminalWorking)
		}
	case SettingIdleArchive:
		return durationValue(copy, input.IdleHours, i18n.CountHour)
	case SettingRetention:
		return durationValue(copy, input.RetentionDays, i18n.CountDay)
	case SettingExpiry:
		return selectedValue(input.RemoveAllOnPurge, copy.Text(i18n.ValueDeleteFiles), copy.Text(i18n.ValueRecordOnly))
	case SettingNotifyFinished:
		return visibilityValue(copy, input.NotifyFinished)
	case SettingNotifyError:
		return visibilityValue(copy, input.NotifyError)
	case SettingNotifyAction:
		return visibilityValue(copy, input.NotifyAction)
	case SettingBackgroundDismiss:
		return fmt.Sprint(input.BackgroundDismiss)
	case SettingNodeSort:
		switch input.NodeSort {
		case "name":
			return copy.Text(i18n.ValueNodeName)
		case "leader":
			return copy.Text(i18n.ValueNodeLeader)
		default:
			return copy.Text(i18n.ValueNodeCreated)
		}
	case SettingQuotaPoll:
		return copy.Format(i18n.ValueMinuteShort, input.QuotaPollMinutes)
	case SettingVoiceBackend:
		return visibilityValue(copy, input.VoiceBackend != "off")
	case SettingOfflineQueue:
		return fmt.Sprint(input.OfflineQueueLimit)
	default:
		return ""
	}
}

func categoryTitle(copy i18n.Localizer, category SettingsCategory) string {
	switch category {
	case CategoryArchive:
		return copy.Text(i18n.SettingsArchive)
	case CategoryCard:
		return copy.Text(i18n.SettingsCard)
	case CategoryNotifications:
		return copy.Text(i18n.SettingsNotifications)
	case CategoryCluster:
		return copy.Text(i18n.SettingsCluster)
	case CategoryVoice:
		return copy.Text(i18n.SettingsVoice)
	default:
		return copy.Text(i18n.SettingsInterface)
	}
}

func settingsCategoryText(input SettingsInput, category SettingsCategory) string {
	text := "<b>" + categoryTitle(input.Copy, category) + "</b>\n\n" +
		input.Copy.Text(i18n.SettingsCategoryBody)
	if category == CategoryCluster && input.ClusterAccounts != "" {
		text += "\n\n" + input.ClusterAccounts
	}
	return text
}

func visibilityChoices(copy i18n.Localizer, visible bool, action Action) Grid {
	return Grid{Row{
		button(selectedLabel(visible, copy.Text(i18n.ValueOn)), action, "on"),
		button(selectedLabel(!visible, copy.Text(i18n.ValueOff)), action, "off"),
	}}
}

func visibilityValue(copy i18n.Localizer, visible bool) string {
	return selectedValue(visible, copy.Text(i18n.ValueOn), copy.Text(i18n.ValueOff))
}

func selectedLabel(selected bool, value string) string {
	if selected {
		return "• " + value
	}
	return value
}

func selectedValue(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func durationValue(copy i18n.Localizer, value int, countKey i18n.CountKey) string {
	if value == 0 {
		return copy.Text(i18n.ValueUnlimited)
	}
	return copy.Count(countKey, value)
}
