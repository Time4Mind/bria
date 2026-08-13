package telegramui

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/i18n"
)

func TestSettingsRootUsesCCBotCategoryFlowGolden(t *testing.T) {
	screen := RenderSettings(settingsFixture("en"))
	if screen.ParseMode != ParseModeHTML || !strings.HasPrefix(screen.Text, "<b>") {
		t.Fatalf("settings format=%q/%q", screen.ParseMode, screen.Text)
	}
	assertGoldenGrid(t, screen, `[🖥 Interface and language -> settings_cat@interface]
[🧾 Card content -> settings_cat@card]
[🗄 Archive -> settings_cat@archive]
[🔔 Notifications -> settings_cat@notifications]
[🎙 Speech recognition -> settings_cat@voice]
[🖧 Cluster -> settings_cat@cluster]
[← Back -> menu]`)
}

func TestSettingsCategoryShowsCurrentValuesGolden(t *testing.T) {
	screen, err := RenderSettingsCategory(settingsFixture("en"), CategoryArchive)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, screen, `[Auto-archive: Unlimited -> setting@idle_archive]
[Archive retention: Unlimited -> setting@retention]
[On expiry: Delete files -> setting@expiry]
[← Back -> settings]`)
}

func TestInterfaceSettingsExposeResumeSelection(t *testing.T) {
	screen, err := RenderSettingsCategory(settingsFixture("ru"), CategoryInterface)
	if err != nil {
		t.Fatal(err)
	}
	grid := CanonicalGrid(screen.Grid)
	if !strings.Contains(grid, "Предлагать возобновление: Вкл -> setting@resume_selection") {
		t.Fatalf("resume setting missing: %s", grid)
	}
	if !strings.Contains(grid, "Очередь недоступной ноды: 5 -> setting@offline_queue") {
		t.Fatalf("offline queue setting missing: %s", grid)
	}
	setting, err := RenderSetting(settingsFixture("ru"), SettingOfflineQueue)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, setting, `[• 5 -> set_offline_q@5] | [10 -> set_offline_q@10] | [20 -> set_offline_q@20]
[← Назад -> settings_cat@interface]`)
}

func TestSettingChoicesUseDotAndReturnToParentGolden(t *testing.T) {
	screen, err := RenderSetting(settingsFixture("ru"), SettingRetention)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, screen, `[14 дней -> set_retention@14] | [30 дней -> set_retention@30] | [• Не ограничено -> set_retention@unlimited]
[← Назад -> settings_cat@archive]`)
}

func TestCardVisibilitySettingsShowIndependentValuesGolden(t *testing.T) {
	input := settingsFixture("en")
	input.ShowToolResults = false
	screen, err := RenderSettingsCategory(input, CategoryCard)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, screen, `[Tool calls: On -> setting@show_tool_calls]
[Tool results: Off -> setting@show_tool_results]
[Output lines: 15 lines -> setting@tool_output_lines]
[Reasoning: On -> setting@show_thinking]
[Response cards: Keep · paging -> setting@response_cards]
[Terminal snapshots: While working -> setting@terminal_snapshots]
[← Back -> settings]`)

	setting, err := RenderSetting(input, SettingToolResults)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, setting, `[On -> set_results@on] | [• Off -> set_results@off]
[← Back -> settings_cat@card]`)
}

func TestNotificationSettingsAndDismissChoicesGolden(t *testing.T) {
	input := settingsFixture("ru")
	input.NotifyError = false
	input.BackgroundDismiss = 5
	screen, err := RenderSettingsCategory(input, CategoryNotifications)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, screen, `[Фон: задача готова: Вкл -> setting@notify_finished]
[Фон: ошибки: Выкл -> setting@notify_error]
[Фон: требуется действие: Вкл -> setting@notify_action]
[Скрывать после переключений: 5 -> setting@background_dismiss]
[← Назад -> settings]`)
	dismiss, err := RenderSetting(input, SettingBackgroundDismiss)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, dismiss, `[1 -> set_bg_dismiss@1] | [3 -> set_bg_dismiss@3] | [• 5 -> set_bg_dismiss@5] | [10 -> set_bg_dismiss@10]
[← Назад -> settings_cat@notifications]`)
}

func TestClusterSettingsExposeGlobalSortAndPollingGolden(t *testing.T) {
	input := settingsFixture("ru")
	input.ClusterAccounts = "codex · account · 41%"
	screen, err := RenderSettingsCategory(input, CategoryCluster)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, input.ClusterAccounts) {
		t.Fatalf("cluster account summary missing: %q", screen.Text)
	}
	assertGoldenGrid(t, screen, `[⬆ Обновить кластер -> cluster_update]
[＋ Подключить ноду -> cluster_add]
[Сортировка серверов: По времени -> setting@node_sort]
[Опрос лимитов: 10 мин -> setting@quota_poll]
[← Назад -> settings]`)
}

func TestResponseCardChoicesDescribeTheThreeModesConcise(t *testing.T) {
	input := settingsFixture("ru")
	input.ResponseCards = "keep_latest"
	screen, err := RenderSetting(input, SettingResponseCards)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, screen, `[Сохранять · листать -> set_cards@keep_paginated]
[• Сохранять · последняя страница -> set_cards@keep_latest]
[Только последняя · листать -> set_cards@replace_paginated]
[← Назад -> settings_cat@card]`)
}

func TestVoiceSettingsExposeOnlyConfirmedOnOffChoice(t *testing.T) {
	input := settingsFixture("ru")
	input.VoiceBackend = "auto"
	screen, err := RenderSettingsCategory(input, CategoryVoice)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, screen, `[Распознавание речи: Вкл -> setting@voice_backend]
[← Назад -> settings]`)
	setting, err := RenderSetting(input, SettingVoiceBackend)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenGrid(t, setting, `[• Вкл -> set_voice@on] | [Выкл -> set_voice@off]
[← Назад -> settings_cat@voice]`)
}

func TestEveryCatalogSettingHasScreenAndParent(t *testing.T) {
	for _, language := range []string{"en", "ru", "zh"} {
		input := settingsFixture(language)
		for _, descriptor := range settingsCatalog {
			screen, err := RenderSetting(input, descriptor.id)
			if err != nil {
				t.Fatalf("%s/%s: %v", language, descriptor.id, err)
			}
			if err := screen.Validate(); err != nil {
				t.Fatalf("%s/%s invalid: %v", language, descriptor.id, err)
			}
			last := screen.Grid[len(screen.Grid)-1][0]
			if last.Callback.Action != ActionSettingsCategory ||
				last.Callback.Token != OpaqueToken(descriptor.category) {
				t.Fatalf("%s/%s has wrong parent: %#v", language, descriptor.id, last)
			}
		}
	}
}

func TestRussianSettingsContainNoEnglishChrome(t *testing.T) {
	screen, err := RenderSettingsCategory(settingsFixture("ru"), CategoryInterface)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Settings", "Session view", "Back"} {
		if strings.Contains(screen.Text+CanonicalGrid(screen.Grid), forbidden) {
			t.Fatalf("Russian settings contain %q", forbidden)
		}
	}
}

func settingsFixture(language string) SettingsInput {
	return SettingsInput{
		Copy: i18n.For(language), AllHosts: true, ResumeSelection: true, IdleHours: 0,
		ShowToolCalls: true, ShowToolResults: true, ShowThinking: true,
		ToolOutputLines:   15,
		ResponseCards:     "keep_paginated",
		TerminalSnapshots: "working",
		RetentionDays:     0, RemoveAllOnPurge: true,
		NotifyFinished: true, NotifyError: true, NotifyAction: true,
		BackgroundDismiss: 1,
		NodeSort:          "created", QuotaPollMinutes: 10,
		VoiceBackend: "auto", OfflineQueueLimit: 5,
	}
}
