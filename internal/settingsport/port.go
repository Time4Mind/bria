// Package settingsport defines the storage-neutral preferences boundary used
// by Telegram control surfaces.
package settingsport

import (
	"context"

	"bria/internal/domain"
)

type Snapshot struct {
	ContinueExisting          bool
	ScreenEnabled             bool
	CardDetail                string
	ShowTechnicalActions      bool
	NotifyBackgroundQuestions bool
	NotifyBackgroundErrors    bool
	SessionLifetime           string
	QueueLimit                int
	VoiceRecognition          string
}

type Preferences interface {
	Snapshot(context.Context) (Snapshot, error)
	ToggleContinueExisting(context.Context) error
	ToggleScreen(context.Context) error
	ToggleCardDetail(context.Context) error
	ToggleTechnicalActions(context.Context) error
	ToggleBackgroundQuestions(context.Context) error
	ToggleBackgroundErrors(context.Context) error
	SetSessionLifetime(context.Context, string) error
}

type ProviderPreference struct {
	Provider   domain.Provider
	Enabled    bool
	Configured bool
}

type ProviderPreferences interface {
	Snapshot(context.Context) ([]ProviderPreference, error)
	ToggleProvider(context.Context, domain.Provider) error
}
