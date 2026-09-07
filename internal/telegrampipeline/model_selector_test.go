package telegrampipeline_test

import (
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/telegrambridge"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestGlobalModelPickerAcceptsOnlyCurrentSignedPresentation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []telegramui.Action{telegramui.ActionNativeKey, telegramui.ActionModelMenu, telegramui.ActionModelChoice, telegramui.ActionEffortMenu, telegramui.ActionEffortChoice} {
		presented, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, []string{string(sessionID)}, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: action, Label: "model", Target: telegramui.ButtonTarget{SessionSlot: 1, Choice: 2}}}}})
		if err != nil {
			t.Fatal(err)
		}
		carrier := telegramstate.Carrier{ChatID: 42, MessageID: 404}
		registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
		if err := telegrampipeline.BindPresentation(context.Background(), registry, carrier, presented); err != nil {
			t.Fatal(err)
		}
		update := coordinator.Update{ID: 404, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: presented.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "selector", SourceMessageID: 404}
		accepted, err := telegrampipeline.AcceptCallback(context.Background(), update, 7, 42, nil, registry, presenter)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
		effect := telegrampipeline.EffectModelSelector
		if action == telegramui.ActionNativeKey {
			effect = telegrampipeline.EffectNativeKey
		}
		if err != nil || plan.SessionID != sessionID || plan.Effect != effect || plan.Target.Choice != 2 {
			t.Fatalf("plan=%#v err=%v", plan, err)
		}
		update.ID++
		update.CallbackQueryID = "replayed-selector"
		if _, err := telegrampipeline.AcceptCallback(context.Background(), update, 7, 42, nil, registry, presenter); !errors.Is(err, telegrampipeline.ErrReplayedCallback) {
			t.Fatalf("replay error=%v", err)
		}
		update.SourceMessageID++
		if _, err := telegrampipeline.AcceptCallback(context.Background(), update, 7, 42, nil, registry, presenter); !errors.Is(err, telegrampipeline.ErrStaleCallback) {
			t.Fatalf("foreign carrier error=%v", err)
		}
		// A replacement keyboard invalidates the previous signed token even
		// when the user taps on the same carrier message.
		if err := telegrampipeline.BindPresentation(context.Background(), registry, carrier, presented); err != nil {
			t.Fatal(err)
		}
		replacement, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, []string{string(sessionID)}, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: action, Label: "replacement", Target: telegramui.ButtonTarget{SessionSlot: 1, Choice: 3}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := telegrampipeline.BindPresentation(context.Background(), registry, carrier, replacement); err != nil {
			t.Fatal(err)
		}
		update.SourceMessageID = carrier.MessageID
		if _, err := telegrampipeline.AcceptCallback(context.Background(), update, 7, 42, nil, registry, presenter); !errors.Is(err, telegrampipeline.ErrStaleCallback) {
			t.Fatalf("replaced presentation error=%v", err)
		}
	}
}
