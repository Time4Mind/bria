package telegramsettings

import (
	"context"
	"testing"

	"bria/internal/settingsport"
)

type standbyPreferences struct {
	settingsport.Preferences
	toggled bool
}

func (p *standbyPreferences) ToggleStandby(context.Context) error { p.toggled = true; return nil }

func TestStandbyToggleUsesIndependentPreference(t *testing.T) {
	p := &standbyPreferences{}
	if err := Apply(context.Background(), p, nil, "settings_standby"); err != nil || !p.toggled {
		t.Fatalf("standby toggle=%v error=%v", p.toggled, err)
	}
}
