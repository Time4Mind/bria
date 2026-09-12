package telegramcontroller_test

import (
	"context"
	"strings"
	"testing"

	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
)

func (p *testPreferences) CycleScreenImageProfile(context.Context) error {
	switch p.settings.ScreenImageProfile {
	case "full_8", "":
		p.settings.ScreenImageProfile = "compact_8"
	case "compact_8":
		p.settings.ScreenImageProfile = "current"
	default:
		p.settings.ScreenImageProfile = "full_8"
	}
	return nil
}

func TestScreenImageProfileActionCyclesAndRerendersSessionButtons(t *testing.T) {
	preferences := &testPreferences{settings: settingsport.Snapshot{
		CardDetail: "standard", CardPageLimit: 64, ShowTechnicalActions: true,
		SessionLifetime: "never", QueueLimit: 32, VoiceRecognition: "parakeet",
		ScreenImageProfile: "full_8",
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Settings: preferences})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsScreenImageProfile})
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, "75%, 8 цветов") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if preferences.settings.ScreenImageProfile != "compact_8" {
		t.Fatalf("profile=%q, want compact_8", preferences.settings.ScreenImageProfile)
	}
}
