// Package settingscomposition composes neutral Telegram settings ports with
// the canonical local settings and configuration stores.
package settingscomposition

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/providerpreferences"
	"bria/internal/settings"
	"bria/internal/settingsport"
)

// Preferences exposes the exact durable settings.FileStore document through
// the controller-neutral Preferences port.
type Preferences struct{ Store settings.Store }

var _ settingsport.Preferences = Preferences{}
var _ settingsport.AutoApprovalPreferences = Preferences{}

func (p Preferences) Snapshot(ctx context.Context) (settingsport.Snapshot, error) {
	if p.Store == nil {
		return settingsport.Snapshot{}, errors.New("settings store is required")
	}
	current, err := p.Store.Load(ctx)
	if err != nil {
		return settingsport.Snapshot{}, err
	}
	return settingsport.Snapshot{
		ContinueExisting: current.ContinueExisting, ScreenEnabled: current.ScreenEnabled, ScreenCaptureLimitKiB: current.ScreenCaptureLimitKiB,
		CardDetail: string(current.CardDetail), CardPageLimit: current.CardPageLimit, ShowTechnicalActions: current.ShowTechnicalActions,
		NotifyBackgroundQuestions: current.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    current.NotifyBackgroundErrors,
		SessionLifetime:           string(current.SessionLifetime), QueueLimit: current.QueueLimit,
		VoiceRecognition:         string(current.VoiceRecognition),
		ArchiveRecommendations:   current.ArchiveRecommendations,
		DefaultProviders:         providerDefaults(current.DefaultProviders),
		DefaultWorkdirs:          workdirDefaults(current.DefaultWorkdirs),
		PreprocessingEnabled:     current.PreprocessingEnabled,
		PreprocessingInstruction: current.PreprocessingInstruction,
		SessionNamingEnabled:     current.SessionNamingEnabled,
		StandbyEnabled:           current.StandbyEnabled,
		AutoApproveCommands:      current.AutoApproveCommands,
	}, nil
}

func (p Preferences) ToggleContinueExisting(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.ContinueExisting = !current.ContinueExisting })
}

func (p Preferences) ToggleStandby(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.StandbyEnabled = !current.StandbyEnabled })
}
func (p Preferences) ToggleAutoApproveCommands(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.AutoApproveCommands = !current.AutoApproveCommands })
}
func (p Preferences) ToggleScreen(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.ScreenEnabled = !current.ScreenEnabled })
}
func (p Preferences) CycleScreenCaptureLimit(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) {
		switch current.ScreenCaptureLimitKiB {
		case 48:
			current.ScreenCaptureLimitKiB = 64
		case 64:
			current.ScreenCaptureLimitKiB = 86
		default:
			current.ScreenCaptureLimitKiB = 48
		}
	})
}
func (p Preferences) ToggleCardDetail(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) {
		if current.CardDetail == settings.CardDetailStandard {
			current.CardDetail = settings.CardDetailCompact
		} else {
			current.CardDetail = settings.CardDetailStandard
		}
	})
}
func (p Preferences) CycleCardPageLimit(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) {
		switch current.CardPageLimit {
		case 32:
			current.CardPageLimit = 64
		case 64:
			current.CardPageLimit = 128
		default:
			current.CardPageLimit = 32
		}
	})
}
func (p Preferences) ToggleTechnicalActions(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.ShowTechnicalActions = !current.ShowTechnicalActions })
}
func (p Preferences) ToggleBackgroundQuestions(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) {
		current.NotifyBackgroundQuestions = !current.NotifyBackgroundQuestions
	})
}
func (p Preferences) ToggleBackgroundErrors(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.NotifyBackgroundErrors = !current.NotifyBackgroundErrors })
}
func (p Preferences) SetSessionLifetime(ctx context.Context, lifetime string) error {
	return p.update(ctx, func(current *settings.Settings) { current.SessionLifetime = settings.SessionLifetime(lifetime) })
}
func (p Preferences) ToggleArchiveRecommendations(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.ArchiveRecommendations = !current.ArchiveRecommendations })
}
func (p Preferences) ToggleSessionNaming(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.SessionNamingEnabled = !current.SessionNamingEnabled })
}
func (p Preferences) TogglePreprocessing(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.PreprocessingEnabled = !current.PreprocessingEnabled })
}
func (p Preferences) SetPreprocessingInstruction(ctx context.Context, instruction string) error {
	return p.update(ctx, func(current *settings.Settings) { current.PreprocessingInstruction = instruction })
}
func (p Preferences) SetDefaultProvider(ctx context.Context, computerID domain.ComputerID, provider domain.Provider) error {
	return p.update(ctx, func(current *settings.Settings) {
		if current.DefaultProviders == nil {
			current.DefaultProviders = map[string]string{}
		}
		current.DefaultProviders[string(computerID)] = string(provider)
	})
}
func (p Preferences) ClearDefaultProvider(ctx context.Context, computerID domain.ComputerID) error {
	return p.update(ctx, func(current *settings.Settings) { delete(current.DefaultProviders, string(computerID)) })
}
func (p Preferences) SetDefaultWorkdir(ctx context.Context, computerID domain.ComputerID, workdir string) error {
	return p.update(ctx, func(current *settings.Settings) {
		if current.DefaultWorkdirs == nil {
			current.DefaultWorkdirs = map[string]string{}
		}
		current.DefaultWorkdirs[string(computerID)] = workdir
	})
}
func (p Preferences) ClearDefaultWorkdir(ctx context.Context, computerID domain.ComputerID) error {
	return p.update(ctx, func(current *settings.Settings) { delete(current.DefaultWorkdirs, string(computerID)) })
}

func providerDefaults(source map[string]string) map[domain.ComputerID]domain.Provider {
	result := make(map[domain.ComputerID]domain.Provider, len(source))
	for computerID, provider := range source {
		result[domain.ComputerID(computerID)] = domain.Provider(provider)
	}
	return result
}

func workdirDefaults(source map[string]string) map[domain.ComputerID]string {
	result := make(map[domain.ComputerID]string, len(source))
	for computerID, workdir := range source {
		result[domain.ComputerID(computerID)] = workdir
	}
	return result
}
func (p Preferences) update(ctx context.Context, mutate func(*settings.Settings)) error {
	if p.Store == nil {
		return errors.New("settings store is required")
	}
	return p.Store.Update(ctx, func(current *settings.Settings) error { mutate(current); return nil })
}

// ProviderPreferences retains the composition API for the provider/config adapter.
type ProviderPreferences = providerpreferences.ProviderPreferences
