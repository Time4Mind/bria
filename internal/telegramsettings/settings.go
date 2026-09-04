package telegramsettings

import (
	"context"
	"errors"
	"strings"

	"bria/internal/domain"
	"bria/internal/settingsport"
)

func Apply(ctx context.Context, preferences settingsport.Preferences, providers settingsport.ProviderPreferences, action string) error {
	if action == "settings_provider_codex" || action == "settings_provider_claude" {
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
	case "settings_page_limit":
		return preferences.CycleCardPageLimit(ctx)
	case "settings_technical_actions":
		return preferences.ToggleTechnicalActions(ctx)
	case "settings_background_questions":
		return preferences.ToggleBackgroundQuestions(ctx)
	case "settings_background_errors":
		return preferences.ToggleBackgroundErrors(ctx)
	case "settings_archive_recommendations":
		creation, ok := preferences.(settingsport.CreationPreferences)
		if !ok {
			return errors.New("session creation settings are not configured")
		}
		return creation.ToggleArchiveRecommendations(ctx)
	case "settings_lifetime_never", "settings_lifetime_6h", "settings_lifetime_12h", "settings_lifetime_24h", "settings_lifetime_48h":
		return preferences.SetSessionLifetime(ctx, strings.TrimPrefix(action, "settings_lifetime_"))
	default:
		return errors.New("unsupported settings action")
	}
}
