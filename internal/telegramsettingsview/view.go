// Package telegramsettingsview renders grouped Telegram settings without
// owning or mutating their persistence.
package telegramsettingsview

import (
	"context"
	"fmt"
	"strings"

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
	Text string
	Rows [][]Button
}

func Render() Surface {
	return Surface{Text: "Настройки\n\nВыберите раздел.", Rows: [][]Button{
		{{Label: "🧾 Содержимое карточки", Action: "settings_category", Choice: int(CategoryCard)}},
		{{Label: "🎛 Кнопки сессии", Action: "settings_category", Choice: int(CategorySessionButtons)}},
		{{Label: "🎙 Распознавание речи", Action: "settings_category", Choice: int(CategoryVoice)}},
		{{Label: "✨ Препроцессинг", Action: "settings_category", Choice: int(CategoryPreprocessing)}},
		{{Label: "🗄 Сессии и архив", Action: "settings_category", Choice: int(CategoryArchive)}},
		{{Label: "🔔 Уведомления", Action: "settings_category", Choice: int(CategoryNotifications)}},
		{{Label: "🛠 Создание сессии", Action: "settings_category", Choice: int(CategoryCreation)}},
		{{Label: "🤖 CLI", Action: "settings_category", Choice: int(CategoryProviders)}},
		{{Label: "Меню", Action: "menu_back"}},
	}}
}

func RenderCategory(ctx context.Context, preferences settingsport.Preferences, providers settingsport.ProviderPreferences, queueLimit int, category Category) (Surface, error) {
	current, err := snapshot(ctx, preferences, queueLimit)
	if err != nil {
		return Surface{}, err
	}
	var text string
	var rows [][]Button
	switch category {
	case CategoryCard:
		text = fmt.Sprintf("🧾 Содержимое карточки\n\nДетализация карточки: %s\nЛимит страниц: %d\nТехнические действия: %s", current.CardDetail, current.CardPageLimit, state(current.ShowTechnicalActions, true))
		rows = onePerRow(Button{Label: "Детализация", Action: "settings_detail"}, Button{Label: "Страницы", Action: "settings_page_limit"}, Button{Label: "Технические действия", Action: "settings_technical_actions"})
	case CategorySessionButtons:
		text = "🎛 Кнопки сессии\n\nScreen: " + state(current.ScreenEnabled, false)
		rows = onePerRow(Button{Label: "Screen", Action: "settings_screen"})
	case CategoryVoice:
		text = "🎙 Распознавание речи\n\nДвижок: " + current.VoiceRecognition
	case CategoryPreprocessing:
		text = "✨ Препроцессинг\n\nСостояние: " + state(current.PreprocessingEnabled, false)
		if current.PreprocessingInstruction == "" {
			text += "\nИнструкция: встроенная"
		} else {
			text += "\nИнструкция: пользовательская"
		}
		rows = onePerRow(Button{Label: "Включить / выключить", Action: "settings_preprocessing"}, Button{Label: "Изменить инструкцию", Action: "settings_preprocessing_instruction"}, Button{Label: "Вернуть встроенную", Action: "settings_preprocessing_reset"})
	case CategoryArchive:
		text = fmt.Sprintf("🗄 Сессии и архив\n\nПродолжать существующую: %s\nРекомендации архива: %s\nСрок жизни сессий: %s\nОчередь: %d", state(current.ContinueExisting, false), state(current.ArchiveRecommendations, true), current.SessionLifetime, current.QueueLimit)
		rows = onePerRow(Button{Label: "Продолжение", Action: "settings_continue_existing"}, Button{Label: "Рекомендации архива", Action: "settings_archive_recommendations"})
		rows = append(rows,
			[]Button{{Label: "Никогда", Action: "settings_lifetime_never"}, {Label: "6 ч", Action: "settings_lifetime_6h"}, {Label: "12 ч", Action: "settings_lifetime_12h"}},
			[]Button{{Label: "24 ч", Action: "settings_lifetime_24h"}, {Label: "48 ч", Action: "settings_lifetime_48h"}})
	case CategoryNotifications:
		text = fmt.Sprintf("🔔 Уведомления\n\nФоновые вопросы: %s\nФоновые ошибки: %s", state(current.NotifyBackgroundQuestions, true), state(current.NotifyBackgroundErrors, true))
		rows = onePerRow(Button{Label: "Вопросы", Action: "settings_background_questions"}, Button{Label: "Ошибки", Action: "settings_background_errors"})
	case CategoryCreation:
		text = "🛠 Создание сессии"
		if _, ok := preferences.(settingsport.CreationPreferences); ok {
			rows = onePerRow(Button{Label: "Backend по умолчанию", Action: "settings_default_provider"}, Button{Label: "Папка по умолчанию", Action: "settings_default_workdir"}, Button{Label: "Сбросить значения по умолчанию", Action: "settings_clear_creation_defaults"})
		}
	case CategoryProviders:
		text = "🤖 CLI"
		if providers != nil {
			providerRows, providerText, providerErr := providerSurface(ctx, providers)
			if providerErr != nil {
				return Surface{}, providerErr
			}
			rows, text = append(rows, providerRows...), text+"\n\n"+providerText
		}
		rows = append(rows, []Button{{Label: "Авторизовать Codex", Action: "authorize_codex"}}, []Button{{Label: "Авторизовать Claude", Action: "authorize_claude"}})
	default:
		return Surface{}, fmt.Errorf("unknown settings category %d", category)
	}
	rows = append(rows, []Button{{Label: "Назад", Action: "menu_settings"}})
	return Surface{Text: text, Rows: rows}, nil
}

func CategoryForAction(action string) (Category, bool) {
	switch action {
	case "settings_detail", "settings_page_limit", "settings_technical_actions":
		return CategoryCard, true
	case "settings_screen":
		return CategorySessionButtons, true
	case "settings_preprocessing", "settings_preprocessing_instruction", "settings_preprocessing_reset":
		return CategoryPreprocessing, true
	case "settings_continue_existing", "settings_archive_recommendations", "settings_lifetime_never", "settings_lifetime_6h", "settings_lifetime_12h", "settings_lifetime_24h", "settings_lifetime_48h":
		return CategoryArchive, true
	case "settings_background_questions", "settings_background_errors":
		return CategoryNotifications, true
	case "settings_default_provider", "settings_default_workdir", "settings_clear_creation_defaults":
		return CategoryCreation, true
	case "settings_provider_codex", "settings_provider_claude", "authorize_codex", "authorize_claude":
		return CategoryProviders, true
	default:
		return 0, false
	}
}

func snapshot(ctx context.Context, preferences settingsport.Preferences, queueLimit int) (settingsport.Snapshot, error) {
	if preferences != nil {
		return preferences.Snapshot(ctx)
	}
	return settingsport.Snapshot{ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true, NotifyBackgroundQuestions: true, NotifyBackgroundErrors: true, SessionLifetime: "never", QueueLimit: queueLimit, VoiceRecognition: "parakeet"}, nil
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

func providerSurface(ctx context.Context, providers settingsport.ProviderPreferences) ([][]Button, string, error) {
	preferences, err := providers.Snapshot(ctx)
	if err != nil {
		return nil, "", err
	}
	by := map[domain.Provider]settingsport.ProviderPreference{}
	for _, preference := range preferences {
		by[preference.Provider] = preference
	}
	var rows [][]Button
	var lines []string
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
		lines = append(lines, fmt.Sprintf("%s: %s, %s", provider, status, configured))
	}
	return rows, strings.Join(lines, "\n"), nil
}
