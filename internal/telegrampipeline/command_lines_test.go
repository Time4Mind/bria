package telegrampipeline_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramui"
)

func TestCommandLinesSignedCallbackRoutesAndRejectsUnauthorizedOrStale(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	action := telegramui.Action("settings_technical_command_lines")
	presented, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: action}}}})
	if err != nil {
		t.Fatalf("command control cannot be presented: %v", err)
	}
	button := presented.Markup.InlineKeyboard[0][0]
	if button.Text != "Строки команды" {
		t.Fatalf("label=%q", button.Text)
	}
	wire, err := codec.Decode(button.CallbackData)
	if err != nil || wire.Action != callbacktoken.Action(88) || callbacktoken.ActionSettingsTechnicalOutputLines != 87 || wire.Target != 0 {
		t.Fatalf("wire=%+v err=%v", wire, err)
	}
	global := card()
	global.SessionID = domain.SessionID(telegramui.GlobalSurfaceID)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	if err := registry.Replace(ctx, telegrampipeline.CallbackPresentation{SessionID: global.SessionID, Carrier: global.Carrier, TokenIDs: presented.TokenIDs, ExpiresAt: presented.ExpiresAt}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                 string
		actor, chat, message int64
		want                 error
	}{
		{"owner", 8, 42, 99, telegrampipeline.ErrNotOwner},
		{"chat", 7, 41, 99, telegrampipeline.ErrNotPrivate},
		{"stale carrier", 7, 42, 100, telegrampipeline.ErrStaleCallback},
		{"valid", 7, 42, 99, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := update(tc.actor, tc.chat, tc.message)
			u.ID, u.Text = 101, button.CallbackData
			accepted, err := telegrampipeline.AcceptCallback(ctx, u, 7, 42, cards{global}, registry, presenter)
			if !errors.Is(err, tc.want) {
				t.Fatalf("accept=%v want=%v", err, tc.want)
			}
			if tc.want != nil {
				return
			}
			plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
			if err != nil || plan.Effect != telegrampipeline.EffectChangeSettings || plan.Action != action {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			if _, err := telegrampipeline.AcceptCallback(ctx, u, 7, 42, cards{global}, registry, presenter); !errors.Is(err, telegrampipeline.ErrReplayedCallback) {
				t.Fatalf("replay=%v", err)
			}
			accepted.Target.Choice = 3
			if _, err := telegrampipeline.PlanAcceptedCallback(accepted); err == nil {
				t.Fatal("cycle accepted a forged choice target")
			}
		})
	}
	replacement, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionSettingsTechnicalOutputLines}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Replace(ctx, telegrampipeline.CallbackPresentation{SessionID: global.SessionID, Carrier: global.Carrier, TokenIDs: replacement.TokenIDs, ExpiresAt: replacement.ExpiresAt}); err != nil {
		t.Fatal(err)
	}
	stale := update(7, 42, 99)
	stale.ID, stale.Text, stale.CallbackQueryID = 102, button.CallbackData, "stale-command-query"
	if _, err := telegrampipeline.AcceptCallback(ctx, stale, 7, 42, cards{global}, registry, presenter); !errors.Is(err, telegrampipeline.ErrStaleCallback) {
		t.Fatalf("replaced presentation=%v", err)
	}
}
