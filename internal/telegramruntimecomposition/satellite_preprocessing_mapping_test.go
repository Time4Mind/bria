package telegramruntimecomposition

import (
	"bytes"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestSatellitePreprocessingModesSignedTransportRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{11}, 32), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		kind   telegramcontroller.SemanticActionKind
		action telegramui.Action
	}{
		{name: "disabled", kind: telegramcontroller.SemanticSettingsPreprocessingDisabled, action: telegramui.ActionSettingsPreprocessingDisabled},
		{name: "shared", kind: telegramcontroller.SemanticSettingsPreprocessingShared, action: telegramui.ActionSettingsPreprocessingShared},
		{name: "per session", kind: telegramcontroller.SemanticSettingsPreprocessingPerSession, action: telegramui.ActionSettingsPreprocessingPerSession},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{
				Text: "Препроцессинг",
				Rows: [][]telegramcontroller.SemanticButton{{{
					Label:  test.name,
					Action: test.kind,
				}}},
			})
			if err != nil {
				t.Fatalf("project semantic surface: %v", err)
			}
			if got := surface.Keyboard.Rows[0][0].Action; got != test.action {
				t.Fatalf("projected action = %q, want %q", got, test.action)
			}

			markup, err := presenter.PresentKeyboard(telegramui.GlobalSurfaceID, nil, surface.Keyboard)
			if err != nil {
				t.Fatalf("present signed keyboard: %v", err)
			}
			if got := markup.InlineKeyboard[0][0].Text; got != test.name {
				t.Fatalf("presented label = %q, want %q", got, test.name)
			}
			decoded, err := presenter.DecodeCallback(markup.InlineKeyboard[0][0].CallbackData)
			if err != nil {
				t.Fatalf("decode signed callback: %v", err)
			}
			if decoded.Action != test.action || decoded.SessionID != telegramui.GlobalSurfaceID {
				t.Fatalf("decoded callback = %#v, want action %q on global surface", decoded, test.action)
			}

			plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
				UpdateID:  1,
				SessionID: domain.SessionID(decoded.SessionID),
				Carrier:   telegramstate.Carrier{ChatID: 1, MessageID: 2},
				Action:    decoded.Action,
				Target:    decoded.Target,
			})
			if err != nil {
				t.Fatalf("plan accepted callback: %v", err)
			}
			if plan.Effect != telegrampipeline.EffectChangeSettings {
				t.Fatalf("callback effect = %q, want %q", plan.Effect, telegrampipeline.EffectChangeSettings)
			}

			semantic, err := semanticActionFromPlan(plan)
			if err != nil {
				t.Fatalf("map callback plan to semantic action: %v", err)
			}
			if semantic.Kind != test.kind || semantic.SessionID != "" {
				t.Fatalf("semantic action = %#v, want kind %q on global surface", semantic, test.kind)
			}
		})
	}
}
