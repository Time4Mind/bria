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
	card.CarrierRevision = 1 // Initial empty-to-confirmed carrier transition.
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
	card.CarrierRevision = 2
	card.CarrierOperation = prepared.OperationID
	card.LastPresentationOperation = prepared.OperationID
	if got, _ := state.Card(id); !reflect.DeepEqual(got, card) || state.ActiveSession != active {
		t.Fatalf("carrier commit changed semantic state: %#v active=%q", got, state.ActiveSession)
	}
	if err := finalizePrepared(ctx, registry, store, prepared, 99); err != nil {
		t.Fatalf("receipt replay not idempotent: %v", err)
	}
}

func TestRecoveredOlderStatusReceiptCannotRollbackNewerSameCarrierProjection(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	presenter := nativeCarrierPresenter(t)
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	store := telegramstate.NewMemoryStore()
	const id = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = id
		return state.SetCard(telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 77},
			CarrierOperation: "status:71", LastPresentationOperation: "operation:old",
			Page: telegramstate.Page{Current: 1, Total: 2}})
	}); err != nil {
		t.Fatal(err)
	}
	prepare := func(operation, expected string, page int, expanded bool) Prepared {
		prepared, err := PrepareCardRefresh(operation, id, 42, 77, telegramui.CardProjectionInput{
			Pages: []telegramui.ContentPage{{Content: "one"}, {Content: "two"}},
			View:  telegramui.PageView{Page: page, Pages: 2},
		}, "", expanded, nil, presenter)
		if err != nil {
			t.Fatal(err)
		}
		revision := uint64(1)
		prepared.Card.ExpectedCarrierRevision = &revision
		prepared.Card.ExpectedPresentationOperation = &expected
		return prepared
	}
	first := prepare("telegram-update:71:prompt-status:preprocessed", "operation:old", 1, false)
	second := prepare("telegram-update:71:prompt-status:accepted", first.OperationID, 2, true)
	for _, prepared := range []Prepared{first, second, first} {
		if err := finalizePrepared(ctx, registry, store, prepared, 77); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := store.Load(ctx)
	card, _ := state.Card(id)
	if card.LastPresentationOperation != second.OperationID || card.Page.Current != 2 || !card.OptionsExpanded {
		t.Fatalf("older receipt rolled projection back: %+v", card)
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
	state := telegramstate.New()
	if err := telegramstate.CommitNativeCarrier(&state, id, telegramstate.Carrier{ChatID: 42, MessageID: 99}, "operation:native"); err == nil {
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
