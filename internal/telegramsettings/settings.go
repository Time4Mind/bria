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
	case "settings_hidden_directories":
		hidden, ok := preferences.(settingsport.HiddenDirectoryPreferences)
		if !ok {
			return errors.New("hidden directory settings are not configured")
		}
		return hidden.ToggleHiddenDirectories(ctx)
	case "settings_standby":
		standby, ok := preferences.(settingsport.StandbyPreferences)
		if !ok {
			return errors.New("standby session settings are not configured")
		}
		return standby.ToggleStandby(ctx)
	case "settings_continue_existing":
		return preferences.ToggleContinueExisting(ctx)
	case "settings_screen":
		return preferences.ToggleScreen(ctx)
	case "settings_screen_capture_limit":
		capture, ok := preferences.(settingsport.ScreenCapturePreferences)
		if !ok {
			return errors.New("screen capture settings are not configured")
		}
		return capture.CycleScreenCaptureLimit(ctx)
	case "settings_detail":
		return preferences.ToggleCardDetail(ctx)
	case "settings_page_limit":
		return preferences.CycleCardPageLimit(ctx)
	case "settings_technical_actions":
		return preferences.ToggleTechnicalActions(ctx)
	case "settings_technical_output_lines":
		output, ok := preferences.(settingsport.TechnicalOutputPreferences)
		if !ok {
			return errors.New("technical output settings are not configured")
		}
		return output.CycleTechnicalOutputLines(ctx)
	case "settings_technical_command_lines":
		command, ok := preferences.(settingsport.TechnicalCommandPreferences)
		if !ok {
			return errors.New("technical command settings are not configured")
		}
		return command.CycleTechnicalCommandLines(ctx)
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
	case "settings_session_naming":
		creation, ok := preferences.(settingsport.CreationPreferences)
		if !ok {
			return errors.New("session creation settings are not configured")
		}
		return creation.ToggleSessionNaming(ctx)
	case "settings_preprocessing":
		preprocessing, ok := preferences.(settingsport.PreprocessingPreferences)
		if !ok {
			return errors.New("preprocessing settings are not configured")
		}
		return preprocessing.TogglePreprocessing(ctx)
	case "settings_preprocessing_reset":
		preprocessing, ok := preferences.(settingsport.PreprocessingPreferences)
		if !ok {
			return errors.New("preprocessing settings are not configured")
		}
		return preprocessing.SetPreprocessingInstruction(ctx, "")
	case "settings_lifetime_never", "settings_lifetime_6h", "settings_lifetime_12h", "settings_lifetime_24h", "settings_lifetime_48h":
		return preferences.SetSessionLifetime(ctx, strings.TrimPrefix(action, "settings_lifetime_"))
	default:
		return errors.New("unsupported settings action")
	}
}
