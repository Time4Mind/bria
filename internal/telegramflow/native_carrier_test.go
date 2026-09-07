package telegramflow

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestNativeReceiptRebindsCarrierWithoutChangingTimelineOrActiveSession(t *testing.T) {
	ctx := context.Background()
	id := domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	active := domain.SessionID("123e4567-e89b-12d3-a456-426614174001")
	store := telegramstate.NewMemoryStore()
	card := telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 10}, Page: telegramstate.Page{Current: 2, Total: 3, Anchor: "a", FollowLatest: false}, OptionsExpanded: true, History: []string{"🙋 prompt", "answer"}, HistoryKeys: []string{"message-1", ""}, EmptyCloseEligible: false}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		if err := state.SetCard(card); err != nil {
			return err
		}
		if err := state.SetCard(telegramstate.Card{SessionID: active, Page: telegramstate.Page{Current: 1, Total: 1}}); err != nil {
			return err
		}
		state.ActiveSession = active
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	presenter := nativeCarrierPresenter(t)
	surface := nativeCarrierSurface(id)
	prepared, err := PrepareSurface("native-carrier", 42, "", 0, false, surface, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Presentation.SessionID != telegramui.GlobalSurfaceID {
		t.Fatal("native carrier changed global signing semantics")
	}
	encoded, err := json.Marshal(clonePrepared(prepared))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Surface.NativeSessionID != id {
		t.Fatal("durable prepared lost native session identity")
	}
	before, _ := store.Load(ctx)
	if got, _ := before.Card(id); !reflect.DeepEqual(got, card) {
		t.Fatal("preparation mutated carrier before receipt")
	}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return time.Unix(1_800_000_000, 0) })
	if err := finalizePrepared(ctx, registry, store, prepared, 0); err == nil {
		t.Fatal("unconfirmed receipt moved carrier")
	}
	if err := finalizePrepared(ctx, registry, store, prepared, 99); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card.Carrier.MessageID = 99
	if got, _ := state.Card(id); !reflect.DeepEqual(got, card) || state.ActiveSession != active {
		t.Fatalf("carrier commit changed semantic state: %#v active=%q", got, state.ActiveSession)
	}
	if err := finalizePrepared(ctx, registry, store, prepared, 99); err != nil {
		t.Fatalf("receipt replay not idempotent: %v", err)
	}
}

func TestNativeSurfaceRejectsMalformedAndCrossSessionBindings(t *testing.T) {
	id := domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	for _, bad := range []domain.SessionID{"not-a-session", domain.SessionID(telegramui.GlobalSurfaceID), "123e4567-e89b-12d3-a456-426614174001"} {
		surface := nativeCarrierSurface(id)
		surface.NativeSessionID = bad
		if _, err := PrepareSurface("bad", 42, "", 0, false, surface, nativeCarrierPresenter(t)); err == nil {
			t.Fatalf("invalid native session %q accepted", bad)
		}
	}
	surface := nativeCarrierSurface(id)
	surface.InteractionSessionID = id
	surface.InteractionRequestID = "request"
	if _, err := PrepareSurface("mixed", 42, "", 0, false, surface, nativeCarrierPresenter(t)); err == nil {
		t.Fatal("mixed native+interaction binding accepted")
	}
	if err := commitNativeCarrier(context.Background(), telegramstate.NewMemoryStore(), id, telegramstate.Carrier{ChatID: 42, MessageID: 99}); err == nil {
		t.Fatal("receipt invented a deleted session")
	}
}

func nativeCarrierSurface(id domain.SessionID) SurfaceOutput {
	return SurfaceOutput{Text: "Select model", NativeSessionID: id, SelectableSessionIDs: []domain.SessionID{id}, Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionNativeKey, Label: "↑", Target: telegramui.ButtonTarget{SessionSlot: 1, Choice: 1}}, {Action: telegramui.ActionMenuBack}}}}}
}

func nativeCarrierPresenter(t *testing.T) *telegrambridge.Presenter {
	t.Helper()
	now := func() time.Time { return time.Unix(1_800_000_000, 0) }
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 4096)), now)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return presenter
}
