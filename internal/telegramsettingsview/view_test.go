package telegramsettingsview

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/settingsport"
)

func TestRenderGroupsSettingsLikeLegacyNavigation(t *testing.T) {
	want := [][]Button{
		{{Label: "🧾 Содержимое карточки", Action: "settings_category", Choice: int(CategoryCard)}},
		{{Label: "🎛 Кнопки сессии", Action: "settings_category", Choice: int(CategorySessionButtons)}},
		{{Label: "🎙 Распознавание речи", Action: "settings_category", Choice: int(CategoryVoice)}},
		{{Label: "✨ Препроцессинг", Action: "settings_category", Choice: int(CategoryPreprocessing)}},
		{{Label: "🗄 Сессии и архив", Action: "settings_category", Choice: int(CategoryArchive)}},
		{{Label: "🔔 Уведомления", Action: "settings_category", Choice: int(CategoryNotifications)}},
		{{Label: "🛠 Создание сессии", Action: "settings_category", Choice: int(CategoryCreation)}},
		{{Label: "🤖 CLI", Action: "settings_category", Choice: int(CategoryProviders)}},
		{{Label: "Меню", Action: "menu_back"}},
	}
	if surface := Render(); !surface.RichMarkdown || !stringsContains(surface.Text, "| Раздел | Содержимое |") || !reflect.DeepEqual(surface.Rows, want) {
		t.Fatalf("Render() = %#v, want rows %#v", surface, want)
	}
}

func TestMissingPreferencesDisableBackgroundQuestions(t *testing.T) {
	surface, err := RenderCategory(context.Background(), nil, nil, 16, CategoryNotifications)
	if err != nil || !stringsContains(surface.Text, "| Фоновые вопросы | выключены |") {
		t.Fatalf("surface=%#v err=%v", surface, err)
	}
}

func TestRenderCategoryKeepsEveryCurrentSettingInOneIntuitiveGroup(t *testing.T) {
	tests := []struct {
		category Category
		contains []string
		actions  []string
	}{
		{CategoryCard, []string{"Содержимое карточки", "| Детализация карточки | standard |", "| Лимит страниц | 64 |", "| Технические действия | включены |"}, []string{"settings_detail", "settings_page_limit", "settings_technical_actions", "menu_settings"}},
		{CategorySessionButtons, []string{"Кнопки сессии", "| Screen | выключено |", "| Размер захвата | 48 KiB |"}, []string{"settings_screen", "settings_screen_capture_limit", "menu_settings"}},
		{CategoryVoice, []string{"Распознавание речи", "| Движок | parakeet |"}, []string{"menu_settings"}},
		{CategoryPreprocessing, []string{"Препроцессинг", "| Режим сателлита | Выключен |", "| Инструкция | встроенная |"}, []string{"settings_preprocessing_disabled", "settings_preprocessing_shared", "settings_preprocessing_per_session", "settings_preprocessing_instruction", "settings_preprocessing_reset", "menu_settings"}},
		{CategoryArchive, []string{"Сессии и архив", "| Продолжать текущую | включено |", "| Рекомендации архива | выключены |", "| Срок жизни сессий | never |", "| Очередь | 16 |"}, []string{"settings_continue_existing", "settings_archive_recommendations", "settings_lifetime_never", "settings_lifetime_6h", "settings_lifetime_12h", "settings_lifetime_24h", "settings_lifetime_48h", "menu_settings"}},
		{CategoryNotifications, []string{"Уведомления", "| Фоновые вопросы | включены |", "| Фоновые ошибки | включены |"}, []string{"settings_background_questions", "settings_background_errors", "menu_settings"}},
		{CategoryCreation, []string{"Создание сессии", "| Автоимя дешёвой моделью | выключено |", "| Ожидающая сессия | выключено |"}, []string{"settings_session_naming", "settings_default_provider", "settings_clear_creation_defaults", "settings_standby", "settings_default_workdir", "menu_settings"}},
		{CategoryProviders, []string{"CLI"}, []string{"authorize_codex", "authorize_claude", "menu_settings"}},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.category), func(t *testing.T) {
			surface, err := RenderCategory(context.Background(), settingsPreferencesStub{}, nil, 16, test.category)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range test.contains {
				if !stringsContains(surface.Text, fragment) {
					t.Errorf("text %q does not contain %q", surface.Text, fragment)
				}
			}
			var actions []string
			for _, row := range surface.Rows {
				for _, button := range row {
					actions = append(actions, button.Action)
				}
			}
			if !reflect.DeepEqual(actions, test.actions) {
				t.Fatalf("actions = %#v, want %#v", actions, test.actions)
			}
		})
	}
}

func TestProviderCategoryShowsAutoApprovalToggleAndRoutesAction(t *testing.T) {
	preferences := autoApprovalPreferencesStub{}
	surface, err := RenderCategory(context.Background(), preferences, nil, 16, CategoryProviders)
	if err != nil {
		t.Fatal(err)
	}
	if !stringsContains(surface.Text, "| Автоподтверждение Codex | включено |") {
		t.Fatalf("surface text=%q", surface.Text)
	}
	var actions []string
	for _, row := range surface.Rows {
		for _, button := range row {
			actions = append(actions, button.Action)
		}
	}
	if !stringsContains(strings.Join(actions, ","), "settings_auto_approve_commands") {
		t.Fatalf("actions=%v", actions)
	}
	if category, ok := CategoryForAction("settings_auto_approve_commands"); !ok || category != CategoryProviders {
		t.Fatalf("category=(%v,%v)", category, ok)
	}
}

func stringsContains(text, fragment string) bool {
	for start := 0; start+len(fragment) <= len(text); start++ {
		if text[start:start+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

type settingsPreferencesStub struct{}

type autoApprovalPreferencesStub struct{ settingsPreferencesStub }

func (autoApprovalPreferencesStub) Snapshot(context.Context) (settingsport.Snapshot, error) {
	return settingsport.Snapshot{ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true, NotifyBackgroundQuestions: true, NotifyBackgroundErrors: true, SessionLifetime: "never", QueueLimit: 16, VoiceRecognition: "parakeet", AutoApproveCommands: true}, nil
}

func (autoApprovalPreferencesStub) ToggleAutoApproveCommands(context.Context) error { return nil }

func (settingsPreferencesStub) Snapshot(context.Context) (settingsport.Snapshot, error) {
	return settingsport.Snapshot{ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true, NotifyBackgroundQuestions: true, NotifyBackgroundErrors: true, SessionLifetime: "never", QueueLimit: 16, VoiceRecognition: "parakeet"}, nil
}
func (settingsPreferencesStub) ToggleContinueExisting(context.Context) error     { return nil }
func (settingsPreferencesStub) ToggleScreen(context.Context) error               { return nil }
func (settingsPreferencesStub) ToggleCardDetail(context.Context) error           { return nil }
func (settingsPreferencesStub) CycleCardPageLimit(context.Context) error         { return nil }
func (settingsPreferencesStub) ToggleTechnicalActions(context.Context) error     { return nil }
func (settingsPreferencesStub) ToggleBackgroundQuestions(context.Context) error  { return nil }
func (settingsPreferencesStub) ToggleBackgroundErrors(context.Context) error     { return nil }
func (settingsPreferencesStub) SetSessionLifetime(context.Context, string) error { return nil }
func (settingsPreferencesStub) ToggleArchiveRecommendations(context.Context) error {
	return nil
}
func (settingsPreferencesStub) ToggleSessionNaming(context.Context) error { return nil }
func (settingsPreferencesStub) ToggleStandby(context.Context) error       { return nil }
func (settingsPreferencesStub) SetDefaultProvider(context.Context, domain.ComputerID, domain.Provider) error {
	return nil
}
func (settingsPreferencesStub) ClearDefaultProvider(context.Context, domain.ComputerID) error {
	return nil
}
func (settingsPreferencesStub) SetDefaultWorkdir(context.Context, domain.ComputerID, string) error {
	return nil
}
func (settingsPreferencesStub) ClearDefaultWorkdir(context.Context, domain.ComputerID) error {
	return nil
}
func (settingsPreferencesStub) TogglePreprocessing(context.Context) error { return nil }
func (settingsPreferencesStub) SetPreprocessingInstruction(context.Context, string) error {
	return nil
}
