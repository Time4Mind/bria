package telegramcreationview_test

import (
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/telegramcreationview"
)

func TestProviderSelectionUsesCLINameAndExactProviderRoutes(t *testing.T) {
	surface := telegramcreationview.Render(sessioncreation.Snapshot{
		Step: sessioncreation.StepProvider,
		Providers: []sessioncreation.ProviderCapability{
			{Provider: domain.ProviderCodex, Installed: true, Enabled: true},
			{Provider: domain.ProviderClaude, Installed: true, Enabled: true},
		},
	})
	if !strings.Contains(surface.Text, "Выберите CLI") || strings.Contains(surface.Text, "бэкенд") {
		t.Fatalf("provider text = %q", surface.Text)
	}
	want := map[string]string{"Codex": "create_select_codex", "Claude": "create_select_claude"}
	for _, row := range surface.Rows {
		for _, button := range row {
			if action, ok := want[button.Label]; ok {
				if button.Action != action {
					t.Fatalf("%s action = %q, want %q", button.Label, button.Action, action)
				}
				delete(want, button.Label)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing CLI buttons: %#v", want)
	}
}

func TestMissingProviderUsesCLINameAndSettingsRoute(t *testing.T) {
	surface := telegramcreationview.Render(sessioncreation.Snapshot{Step: sessioncreation.StepInstallRequired})
	if !strings.Contains(surface.Text, "CLI") || strings.Contains(surface.Text, "бэкенд") {
		t.Fatalf("install-required text = %q", surface.Text)
	}
	if len(surface.Rows) == 0 || len(surface.Rows[0]) != 1 || surface.Rows[0][0].Action != "menu_settings" {
		t.Fatalf("settings route = %#v", surface.Rows)
	}
}
