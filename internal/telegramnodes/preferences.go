package telegramnodes

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/settingsport"
)

type ProviderPreferences struct {
	Current     func() domain.ComputerID
	LocalNode   domain.ComputerID
	Local       settingsport.ProviderPreferences
	Environment sessioncreation.Environment
}

func AvailableProviders(ctx context.Context, preferences settingsport.ProviderPreferences) ([]domain.Provider, error) {
	known := []domain.Provider{domain.ProviderCodex, domain.ProviderClaude}
	if preferences == nil {
		return known, nil
	}
	snapshot, err := preferences.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	available := make(map[domain.Provider]bool, len(snapshot))
	for _, preference := range snapshot {
		available[preference.Provider] = preference.Configured && preference.Enabled
	}
	result := make([]domain.Provider, 0, len(known))
	for _, provider := range known {
		if available[provider] {
			result = append(result, provider)
		}
	}
	return result, nil
}

func ProviderEnabled(ctx context.Context, environment sessioncreation.Environment, local domain.ComputerID, preferences settingsport.ProviderPreferences, nodeID domain.ComputerID, provider domain.Provider) (bool, error) {
	if environment != nil {
		nodes, err := environment.AvailableComputers(ctx)
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.ID != nodeID {
				continue
			}
			for _, capability := range node.Capabilities {
				if capability.Provider == provider {
					return capability.Installed && capability.Enabled, nil
				}
			}
			return false, nil
		}
		return false, nil
	}
	if nodeID != local {
		return false, nil
	}
	providers, err := AvailableProviders(ctx, preferences)
	if err != nil {
		return false, err
	}
	for _, available := range providers {
		if available == provider {
			return true, nil
		}
	}
	return false, nil
}

func (preferences ProviderPreferences) Snapshot(ctx context.Context) ([]settingsport.ProviderPreference, error) {
	nodeID := preferences.Current()
	if nodeID == preferences.LocalNode && preferences.Local != nil {
		return preferences.Local.Snapshot(ctx)
	}
	if preferences.Environment == nil {
		return nil, errors.New("selected node provider settings are unavailable")
	}
	nodes, err := preferences.Environment.AvailableComputers(ctx)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if node.ID != nodeID {
			continue
		}
		result := make([]settingsport.ProviderPreference, 0, len(node.Capabilities))
		for _, capability := range node.Capabilities {
			result = append(result, settingsport.ProviderPreference{Provider: capability.Provider, Configured: capability.Installed, Enabled: capability.Enabled})
		}
		return result, nil
	}
	return nil, errors.New("selected node provider settings are unavailable")
}

func (preferences ProviderPreferences) ToggleProvider(ctx context.Context, provider domain.Provider) error {
	return preferences.ToggleAt(ctx, preferences.Current(), provider)
}

func (preferences ProviderPreferences) ToggleAt(ctx context.Context, nodeID domain.ComputerID, provider domain.Provider) error {
	if nodeID == preferences.LocalNode && preferences.Local != nil {
		return preferences.Local.ToggleProvider(ctx, provider)
	}
	if activator, ok := preferences.Environment.(sessioncreation.ProviderActivator); ok {
		return activator.ToggleProvider(ctx, nodeID, provider)
	}
	return errors.New("selected node provider activation is unavailable")
}
