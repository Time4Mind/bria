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
	CardPageLimit             int
	ShowTechnicalActions      bool
	NotifyBackgroundQuestions bool
	NotifyBackgroundErrors    bool
	SessionLifetime           string
	QueueLimit                int
	VoiceRecognition          string
	ArchiveRecommendations    bool
	DefaultProviders          map[domain.ComputerID]domain.Provider
	DefaultWorkdirs           map[domain.ComputerID]string
}

type Preferences interface {
	Snapshot(context.Context) (Snapshot, error)
	ToggleContinueExisting(context.Context) error
	ToggleScreen(context.Context) error
	ToggleCardDetail(context.Context) error
	CycleCardPageLimit(context.Context) error
	ToggleTechnicalActions(context.Context) error
	ToggleBackgroundQuestions(context.Context) error
	ToggleBackgroundErrors(context.Context) error
	SetSessionLifetime(context.Context, string) error
}

// CreationPreferences is optional while older compositions expose only the
// established general settings contract.
type CreationPreferences interface {
	ToggleArchiveRecommendations(context.Context) error
	SetDefaultProvider(context.Context, domain.ComputerID, domain.Provider) error
	ClearDefaultProvider(context.Context, domain.ComputerID) error
	SetDefaultWorkdir(context.Context, domain.ComputerID, string) error
	ClearDefaultWorkdir(context.Context, domain.ComputerID) error
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
