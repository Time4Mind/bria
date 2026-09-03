package telegramsettings

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bria/internal/domain"
	"bria/internal/settingsport"
)

type Button struct{ Label, Action string }
type Surface struct {
	Text string
	Rows [][]Button
}

func Render(ctx context.Context, preferences settingsport.Preferences, providers settingsport.ProviderPreferences, queueLimit int) (Surface, error) {
	current := settingsport.Snapshot{ContinueExisting: true, CardDetail: "standard", ShowTechnicalActions: true, NotifyBackgroundQuestions: true, NotifyBackgroundErrors: true, SessionLifetime: "never", QueueLimit: queueLimit, VoiceRecognition: "parakeet"}
	if preferences != nil {
		var err error
		current, err = preferences.Snapshot(ctx)
		if err != nil {
			return Surface{}, err
		}
	}
	rows := [][]Button{{{"Продолжение", "settings_continue_existing"}, {"Screen", "settings_screen"}}, {{"Детализация", "settings_detail"}, {"Тех. действия", "settings_technical_actions"}}, {{"Вопросы", "settings_background_questions"}, {"Ошибки", "settings_background_errors"}}, {{"Никогда", "settings_lifetime_never"}, {"6 ч", "settings_lifetime_6h"}, {"12 ч", "settings_lifetime_12h"}}, {{"24 ч", "settings_lifetime_24h"}, {"48 ч", "settings_lifetime_48h"}}}
	text := format(current)
	if providers != nil {
		ps, err := providers.Snapshot(ctx)
		if err != nil {
			return Surface{}, err
		}
		providerRows, providerText := providerSurface(ps)
		rows = append(rows, providerRows...)
		text += "\n" + providerText
	}
	rows = append(rows, []Button{{"Авторизовать Codex", "authorize_codex"}, {"Авторизация Claude", "authorize_claude"}}, []Button{{"Меню", "menu_back"}})
	return Surface{Text: text, Rows: rows}, nil
}

func Apply(ctx context.Context, preferences settingsport.Preferences, providers settingsport.ProviderPreferences, action string) error {
	switch action {
	case "settings_provider_codex", "settings_provider_claude":
		if providers == nil {
			return errors.New("provider settings are not configured")
		}
		provider := domain.ProviderCodex
		if action == "settings_provider_claude" {
			provider = domain.ProviderClaude
		}
		return providers.ToggleProvider(ctx, provider)
	}
	if preferences == nil {
		return errors.New("settings are not configured")
	}
	switch action {
	case "settings_continue_existing":
		return preferences.ToggleContinueExisting(ctx)
	case "settings_screen":
		return preferences.ToggleScreen(ctx)
	case "settings_detail":
		return preferences.ToggleCardDetail(ctx)
	case "settings_technical_actions":
		return preferences.ToggleTechnicalActions(ctx)
	case "settings_background_questions":
		return preferences.ToggleBackgroundQuestions(ctx)
	case "settings_background_errors":
		return preferences.ToggleBackgroundErrors(ctx)
	case "settings_lifetime_never":
		return preferences.SetSessionLifetime(ctx, "never")
	case "settings_lifetime_6h":
		return preferences.SetSessionLifetime(ctx, "6h")
	case "settings_lifetime_12h":
		return preferences.SetSessionLifetime(ctx, "12h")
	case "settings_lifetime_24h":
		return preferences.SetSessionLifetime(ctx, "24h")
	case "settings_lifetime_48h":
		return preferences.SetSessionLifetime(ctx, "48h")
	default:
		return errors.New("unsupported settings action")
	}
}
func format(s settingsport.Snapshot) string {
	return fmt.Sprintf("Настройки:\nПродолжать существующую: %t\nScreen: %t\nДетализация карточки: %s\nТехнические действия: %t\nФоновые вопросы: %t\nФоновые ошибки: %t\nСрок жизни сессий: %s\nОчередь: %d\nГолос: %s", s.ContinueExisting, s.ScreenEnabled, s.CardDetail, s.ShowTechnicalActions, s.NotifyBackgroundQuestions, s.NotifyBackgroundErrors, s.SessionLifetime, s.QueueLimit, s.VoiceRecognition)
}
func providerSurface(ps []settingsport.ProviderPreference) ([][]Button, string) {
	by := map[domain.Provider]settingsport.ProviderPreference{}
	for _, p := range ps {
		by[p.Provider] = p
	}
	lines := []string{"Исполнители:"}
	buttons := []Button{}
	for _, p := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		x := by[p]
		state, configured := "выключен", "не настроен"
		if x.Enabled {
			state = "включен"
		}
		if x.Configured {
			configured = "настроен"
		}
		lines = append(lines, fmt.Sprintf("%s: %s, %s", p, state, configured))
		action := "settings_provider_codex"
		if p == domain.ProviderClaude {
			action = "settings_provider_claude"
		}
		buttons = append(buttons, Button{string(p), action})
	}
	return [][]Button{buttons}, strings.Join(lines, "\n")
}
