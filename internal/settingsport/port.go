// Package settingsport defines the storage-neutral preferences boundary used
// by Telegram control surfaces.
package settingsport

import (
	"context"

	"bria/internal/domain"
	"bria/internal/settingscapability"
)

type Snapshot struct {
	ContinueExisting          bool
	ScreenEnabled             bool
	ScreenCaptureLimitKiB     int
	CardDetail                string
	CardPageLimit             int
	ShowTechnicalActions      bool
	TechnicalOutputLines      int
	NotifyBackgroundQuestions bool
	NotifyBackgroundErrors    bool
	SessionLifetime           string
	QueueLimit                int
	VoiceRecognition          string
	ArchiveRecommendations    bool
	DefaultProviders          map[domain.ComputerID]domain.Provider
	DefaultWorkdirs           map[domain.ComputerID]string
	PreprocessingEnabled      bool
	PreprocessingInstruction  string
	SessionNamingEnabled      bool
	StandbyEnabled            bool
	AutoApproveCommands       bool
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
	ToggleSessionNaming(context.Context) error
	SetDefaultProvider(context.Context, domain.ComputerID, domain.Provider) error
	ClearDefaultProvider(context.Context, domain.ComputerID) error
	SetDefaultWorkdir(context.Context, domain.ComputerID, string) error
	ClearDefaultWorkdir(context.Context, domain.ComputerID) error
}

type PreprocessingPreferences interface {
	TogglePreprocessing(context.Context) error
	SetPreprocessingInstruction(context.Context, string) error
}

type StandbyPreferences interface {
	ToggleStandby(context.Context) error
}

// Optional capability aliases preserve existing consumers.
type AutoApprovalPreferences = settingscapability.AutoApprovalPreferences
type ScreenCapturePreferences = settingscapability.ScreenCapturePreferences
type TechnicalOutputPreferences = settingscapability.TechnicalOutputPreferences

// NodeRenamer optionally persists the display name of a computer node.
type NodeRenamer interface {
	RenameNode(context.Context, domain.ComputerID, string) error
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
