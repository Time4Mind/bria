package telegramsettings

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/settingsport"
)

// NormalizeNodeName returns one safe config and Telegram display value.
func NormalizeNodeName(value string) (string, error) {
	value = strings.TrimSpace(value)
	invalid := func(character rune) bool {
		return unicode.IsControl(character) || unicode.Is(unicode.Zl, character) || unicode.Is(unicode.Zp, character)
	}
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 64 || len(value) > 128 || strings.IndexFunc(value, invalid) >= 0 {
		return "", errors.New("имя должно содержать от 1 до 64 печатных символов")
	}
	return value, nil
}

func ValidateNodeName(value string, current domain.ComputerID, nodes []sessioncreation.Computer) (string, error) {
	name, err := NormalizeNodeName(value)
	if err != nil {
		return "", err
	}
	for _, node := range nodes {
		if node.ID != current && strings.EqualFold(strings.TrimSpace(node.Name), name) {
			return "", errors.New("имя уже используется другой нодой")
		}
	}
	return name, nil
}

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
	case "settings_preprocessing_disabled", "settings_preprocessing_shared", "settings_preprocessing_per_session":
		preprocessing, ok := preferences.(settingsport.SatellitePreprocessingPreferences)
		if !ok {
			return errors.New("satellite preprocessing settings are not configured")
		}
		mode := settingsport.SatellitePreprocessingMode(strings.TrimPrefix(action, "settings_preprocessing_"))
		return preprocessing.SetSatellitePreprocessingMode(ctx, mode)
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
