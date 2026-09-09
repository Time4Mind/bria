// Package telegramsettingsview renders grouped Telegram settings without
// owning or mutating their persistence.
package telegramsettingsview

import (
	"context"
	"fmt"

	"bria/internal/domain"
	"bria/internal/settingsport"
)

type Category int

const (
	CategoryCard Category = iota + 1
	CategorySessionButtons
	CategoryVoice
	CategoryPreprocessing
	CategoryArchive
	CategoryNotifications
	CategoryCreation
	CategoryProviders
)

type Button struct {
	Label, Action string
	Choice        int
}
type Surface struct {
	Text         string
	RichMarkdown bool
	Rows         [][]Button
}

func Render() Surface {
	surface := table("Настройки", "Раздел", "Содержимое",
		Field{"Содержимое карточки", "Детализация, страницы и технические действия"},
		Field{"Кнопки сессии", "Отображение Screen"}, Field{"Распознавание речи", "Движок распознавания"},
		Field{"Препроцессинг", "Состояние и инструкция"}, Field{"Сессии и архив", "Рекомендации, срок жизни и очередь"},
		Field{"Уведомления", "Фоновые вопросы и ошибки"}, Field{"Создание сессии", "Автоимя и значения по умолчанию"}, Field{"CLI", "Включение и авторизация"})
	surface.Rows = [][]Button{
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
	return surface
}

func RenderCategory(ctx context.Context, preferences settingsport.Preferences, providers settingsport.ProviderPreferences, queueLimit int, category Category) (Surface, error) {
	current, err := snapshot(ctx, preferences, queueLimit)
	if err != nil {
		return Surface{}, err
	}
	var text string
	var fields []Field
	var rows [][]Button
	switch category {
	case CategoryCard:
		text = "🧾 Содержимое карточки"
		fields = []Field{{"Детализация карточки", current.CardDetail}, {"Лимит страниц", fmt.Sprint(current.CardPageLimit)}, {"Технические действия", state(current.ShowTechnicalActions, true)}}
		rows = onePerRow(Button{Label: "Детализация", Action: "settings_detail"}, Button{Label: "Страницы", Action: "settings_page_limit"}, Button{Label: "Технические действия", Action: "settings_technical_actions"})
		commandLines := current.TechnicalCommandLines
		if commandLines == 0 {
			commandLines = 10
		}
		fields = append(fields, Field{"Строки команды", fmt.Sprint(commandLines)})
		if _, ok := preferences.(settingsport.TechnicalCommandPreferences); ok {
			rows = append(rows, []Button{{Label: "Строки команды", Action: "settings_technical_command_lines"}})
		}
		lines := current.TechnicalOutputLines
		if lines == 0 {
			lines = 10
		}
		fields = append(fields, Field{"Строки технического вывода", fmt.Sprint(lines)})
		if _, ok := preferences.(settingsport.TechnicalOutputPreferences); ok {
			rows = append(rows, []Button{{Label: "Строки технического вывода", Action: "settings_technical_output_lines"}})
		}
	case CategorySessionButtons:
		text = "🎛 Кнопки сессии"
		captureLimit := current.ScreenCaptureLimitKiB
		if captureLimit == 0 {
			captureLimit = 48
		}
		fields = []Field{{"Screen", state(current.ScreenEnabled, false)}, {"Размер захвата", fmt.Sprintf("%d KiB", captureLimit)}}
		rows = onePerRow(Button{Label: "Screen", Action: "settings_screen"}, Button{Label: "Размер захвата", Action: "settings_screen_capture_limit"})
	case CategoryVoice:
		text = "🎙 Распознавание речи"
		fields = []Field{{"Движок", current.VoiceRecognition}}
	case CategoryPreprocessing:
		text = "✨ Препроцессинг"
		fields = []Field{{"Состояние", state(current.PreprocessingEnabled, false)}}
		if current.PreprocessingInstruction == "" {
			fields = append(fields, Field{"Инструкция", "встроенная"})
		} else {
			fields = append(fields, Field{"Инструкция", "пользовательская"})
		}
		rows = onePerRow(Button{Label: "Включить / выключить", Action: "settings_preprocessing"}, Button{Label: "Изменить инструкцию", Action: "settings_preprocessing_instruction"}, Button{Label: "Вернуть встроенную", Action: "settings_preprocessing_reset"})
	case CategoryArchive:
		text = "🗄 Сессии и архив"
		fields = []Field{{"Продолжать текущую", state(current.ContinueExisting, false)}, {"Рекомендации архива", state(current.ArchiveRecommendations, true)}, {"Срок жизни сессий", current.SessionLifetime}, {"Очередь", fmt.Sprint(current.QueueLimit)}}
		rows = onePerRow(Button{Label: "Продолжать текущую", Action: "settings_continue_existing"}, Button{Label: "Рекомендации архива", Action: "settings_archive_recommendations"})
		rows = append(rows,
			[]Button{{Label: "Никогда", Action: "settings_lifetime_never"}, {Label: "6 ч", Action: "settings_lifetime_6h"}, {Label: "12 ч", Action: "settings_lifetime_12h"}},
			[]Button{{Label: "24 ч", Action: "settings_lifetime_24h"}, {Label: "48 ч", Action: "settings_lifetime_48h"}})
	case CategoryNotifications:
		text = "🔔 Уведомления"
		fields = []Field{{"Фоновые вопросы", state(current.NotifyBackgroundQuestions, true)}, {"Фоновые ошибки", state(current.NotifyBackgroundErrors, true)}}
		rows = onePerRow(Button{Label: "Вопросы", Action: "settings_background_questions"}, Button{Label: "Ошибки", Action: "settings_background_errors"})
	case CategoryCreation:
		text = "🛠 Создание сессии"
		fields = []Field{{"Автоимя дешёвой моделью", state(current.SessionNamingEnabled, false)}}
		if _, ok := preferences.(settingsport.CreationPreferences); ok {
			fields = append(fields, Field{"Backend по умолчанию", defaultProviderValue(current.DefaultProviders)}, Field{"Папка по умолчанию", defaultWorkdirValue(current.DefaultWorkdirs)})
			// Keep controls in the same order as the rendered key/value table;
			// the directory default is the final setting before Back.
			rows = onePerRow(Button{Label: "Автоимя", Action: "settings_session_naming"}, Button{Label: "Backend по умолчанию", Action: "settings_default_provider"}, Button{Label: "Сбросить значения по умолчанию", Action: "settings_clear_creation_defaults"})
		}
		fields = append(fields, Field{"Ожидающая сессия", state(current.StandbyEnabled, false)})
		if _, ok := preferences.(settingsport.StandbyPreferences); ok {
			rows = append(rows, []Button{{Label: "Ожидающая сессия", Action: "settings_standby"}})
		}
		hidden := "скрывать"
		if current.ShowHiddenDirectories {
			hidden = "показывать"
		}
		fields = append(fields, Field{"Скрытые каталоги", hidden})
		if _, ok := preferences.(settingsport.HiddenDirectoryPreferences); ok {
			rows = append(rows, []Button{{Label: "Скрытые каталоги", Action: "settings_hidden_directories"}})
		}
		if _, ok := preferences.(settingsport.CreationPreferences); ok {
			rows = append(rows, []Button{{Label: "Папка по умолчанию", Action: "settings_default_workdir"}})
		}
	case CategoryProviders:
		text = "🤖 CLI"
		if providers != nil {
			providerRows, providerFields, providerErr := providerSurface(ctx, providers)
			if providerErr != nil {
				return Surface{}, providerErr
			}
			rows, fields = append(rows, providerRows...), providerFields
		}
		if len(fields) == 0 {
			fields = []Field{{"CLI", "не настроены"}}
		}
		if _, ok := preferences.(settingsport.AutoApprovalPreferences); ok {
			fields = append(fields, Field{"Автоподтверждение Codex", state(current.AutoApproveCommands, false)})
			rows = append(rows, []Button{{Label: "Автоподтверждение Codex", Action: "settings_auto_approve_commands"}})
		}
		rows = append(rows, []Button{{Label: "Авторизовать Codex", Action: "authorize_codex"}}, []Button{{Label: "Авторизовать Claude", Action: "authorize_claude"}})
	default:
		return Surface{}, fmt.Errorf("unknown settings category %d", category)
	}
	rows = append(rows, []Button{{Label: "Назад", Action: "menu_settings"}})
	surface := table(text, "Настройка", "Значение", fields...)
	surface.Rows = rows
	return surface, nil
}

func defaultProviderValue(values map[domain.ComputerID]domain.Provider) string {
	for _, provider := range values {
		return string(provider)
	}
	return "не задан"
}

func defaultWorkdirValue(values map[domain.ComputerID]string) string {
	for _, workdir := range values {
		if workdir != "" {
			return workdir
		}
	}
	return "не задана"
}

func CategoryForAction(action string) (Category, bool) {
	switch action {
	case "settings_detail", "settings_page_limit", "settings_technical_actions", "settings_technical_output_lines", "settings_technical_command_lines":
		return CategoryCard, true
	case "settings_screen", "settings_screen_capture_limit":
		return CategorySessionButtons, true
	case "settings_preprocessing", "settings_preprocessing_instruction", "settings_preprocessing_reset":
		return CategoryPreprocessing, true
	case "settings_continue_existing", "settings_archive_recommendations", "settings_lifetime_never", "settings_lifetime_6h", "settings_lifetime_12h", "settings_lifetime_24h", "settings_lifetime_48h":
		return CategoryArchive, true
	case "settings_background_questions", "settings_background_errors":
		return CategoryNotifications, true
	case "settings_hidden_directories", "settings_standby", "settings_session_naming", "settings_default_provider", "settings_default_workdir", "settings_clear_creation_defaults", "settings_rename_node":
		return CategoryCreation, true
	case "settings_provider_codex", "settings_provider_claude", "authorize_codex", "authorize_claude", "settings_auto_approve_commands":
		return CategoryProviders, true
	default:
		return 0, false
	}
}

func snapshot(ctx context.Context, preferences settingsport.Preferences, queueLimit int) (settingsport.Snapshot, error) {
	if preferences != nil {
		return preferences.Snapshot(ctx)
	}
	return settingsport.Snapshot{ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true, NotifyBackgroundQuestions: false, NotifyBackgroundErrors: true, SessionLifetime: "never", QueueLimit: queueLimit, VoiceRecognition: "parakeet", AutoApproveCommands: true}, nil
}

func state(value, plural bool) string {
	if value && plural {
		return "включены"
	}
	if value {
		return "включено"
	}
	if plural {
		return "выключены"
	}
	return "выключено"
}

func onePerRow(buttons ...Button) [][]Button {
	rows := make([][]Button, len(buttons))
	for index, button := range buttons {
		rows[index] = []Button{button}
	}
	return rows
}

func providerSurface(ctx context.Context, providers settingsport.ProviderPreferences) ([][]Button, []Field, error) {
	preferences, err := providers.Snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	by := map[domain.Provider]settingsport.ProviderPreference{}
	for _, preference := range preferences {
		by[preference.Provider] = preference
	}
	var rows [][]Button
	var fields []Field
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		preference := by[provider]
		status, configured := "выключен", "не настроен"
		if preference.Enabled {
			status = "включен"
		}
		if preference.Configured {
			configured = "настроен"
		}
		rows = append(rows, []Button{{Label: string(provider), Action: "settings_provider_" + string(provider)}})
		fields = append(fields, Field{string(provider), status + ", " + configured})
	}
	return rows, fields, nil
}
