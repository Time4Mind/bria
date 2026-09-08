// Package providerpreferences adapts local configuration capability flags and
// node display names to neutral settings ports without accessing credentials.
package providerpreferences

import (
	"context"
	"errors"
	"strings"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/settingsport"
)

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

// RenameNode updates only the non-secret display metadata in the local config.
// It is intentionally exposed through the provider/config composition so the
// Telegram controller does not depend on a concrete config store.
func (p ProviderPreferences) RenameNode(ctx context.Context, nodeID domain.ComputerID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return errors.New("имя ноды должно содержать от 1 до 64 символов")
	}
	if p.Store == nil {
		return errors.New("provider configuration store is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		snapshot, err := p.Store.Current(ctx)
		if err != nil {
			return err
		}
		if snapshot.Config.Computer == nil || domain.ComputerID(snapshot.Config.Computer.ID) != nodeID {
			return errors.New("нода не найдена в локальной конфигурации")
		}
		next := snapshot.Config
		computer := *snapshot.Config.Computer
		computer.Name = name
		next.Computer = &computer
		if _, err = p.Store.CompareAndSwap(ctx, snapshot.Revision, next); err == nil {
			return nil
		}
		if !errors.Is(err, config.ErrRevisionConflict) {
			return err
		}
	}
	return errors.New("не удалось сохранить имя ноды из-за конфликта конфигурации")
}
