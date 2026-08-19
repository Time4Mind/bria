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
	LeaderAutomatic    bool
	PreferredLeader    string
	LeaderNodes        []LeaderSettingNode
	VoiceBackend       string
	OfflineQueueLimit  int
	ClusterAccounts    string
	PendingEnrollments []PendingEnrollmentItem
}

type LeaderSettingNode struct {
	Name     string
	Selected bool
	Disabled bool
	Token    OpaqueToken
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

func RenderVoiceInputEnableConfirmation(copy i18n.Localizer, plans []string) Screen {
	screen := RenderVoiceEnableConfirmation(copy, plans)
	screen.Text = html.EscapeString(copy.Text(i18n.VoiceInputDisabled)) + "\n\n" + screen.Text
	return screen
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
	case SettingLeaderMode:
		return selectedValue(input.LeaderAutomatic, copy.Text(i18n.ValueLeaderAutomatic), copy.Text(i18n.ValueLeaderManual))
	case SettingLeaderNode:
		if input.PreferredLeader == "" {
			return copy.Text(i18n.ValueLeaderUnassigned)
		}
		return input.PreferredLeader
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
	return choiceRows(
		button(selectedLabel(visible, copy.Text(i18n.ValueOn)), action, "on"),
		button(selectedLabel(!visible, copy.Text(i18n.ValueOff)), action, "off"),
	)
}

func choiceRows(buttons ...Button) Grid {
	return settingsRows(buttons)
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
