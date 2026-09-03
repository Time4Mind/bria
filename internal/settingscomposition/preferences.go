// Package settingscomposition composes neutral Telegram settings ports with
// the canonical local settings and configuration stores.
package settingscomposition

import (
	"context"
	"errors"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/settings"
	"bria/internal/settingsport"
)

// Preferences exposes the exact durable settings.FileStore document through
// the controller-neutral Preferences port.
type Preferences struct{ Store settings.Store }

var _ settingsport.Preferences = Preferences{}

func (p Preferences) Snapshot(ctx context.Context) (settingsport.Snapshot, error) {
	if p.Store == nil {
		return settingsport.Snapshot{}, errors.New("settings store is required")
	}
	current, err := p.Store.Load(ctx)
	if err != nil {
		return settingsport.Snapshot{}, err
	}
	return settingsport.Snapshot{
		ContinueExisting: current.ContinueExisting, ScreenEnabled: current.ScreenEnabled,
		CardDetail: string(current.CardDetail), ShowTechnicalActions: current.ShowTechnicalActions,
		NotifyBackgroundQuestions: current.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    current.NotifyBackgroundErrors,
		SessionLifetime:           string(current.SessionLifetime), QueueLimit: current.QueueLimit,
		VoiceRecognition: string(current.VoiceRecognition),
	}, nil
}

func (p Preferences) ToggleContinueExisting(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.ContinueExisting = !current.ContinueExisting })
}
func (p Preferences) ToggleScreen(ctx context.Context) error {
	return p.update(ctx, func(current *settings.Settings) { current.ScreenEnabled = !current.ScreenEnabled })
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
func (p Preferences) update(ctx context.Context, mutate func(*settings.Settings)) error {
	if p.Store == nil {
		return errors.New("settings store is required")
	}
	return p.Store.Update(ctx, func(current *settings.Settings) error { mutate(current); return nil })
}

// ProviderPreferences exposes only provider capability flags and changes them
// through config.FileStore's atomic SetProviderEnabled path. It never reads or
// writes credentials.
type ProviderPreferences struct{ Store config.Store }

var _ settingsport.ProviderPreferences = ProviderPreferences{}

func (p ProviderPreferences) Snapshot(ctx context.Context) ([]settingsport.ProviderPreference, error) {
	if p.Store == nil {
		return nil, errors.New("provider configuration store is required")
	}
	snapshot, err := p.Store.Current(ctx)
	if err != nil {
		return nil, err
	}
	capabilities := snapshot.Config.ProviderCapabilities()
	result := make([]settingsport.ProviderPreference, 0, len(capabilities))
	for _, capability := range capabilities {
		result = append(result, settingsport.ProviderPreference{Provider: capability.Provider, Enabled: capability.Enabled, Configured: capability.Configured})
	}
	return result, nil
}

func (p ProviderPreferences) ToggleProvider(ctx context.Context, provider domain.Provider) error {
	if p.Store == nil {
		return errors.New("provider configuration store is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		snapshot, err := p.Store.Current(ctx)
		if err != nil {
			return err
		}
		_, err = p.Store.SetProviderEnabled(ctx, snapshot.Revision, provider, !snapshot.Config.ProviderEnabled(provider))
		if !errors.Is(err, config.ErrRevisionConflict) {
			return err
		}
	}
	return config.ErrRevisionConflict
}
