package telegrampipeline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

const sessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")

type cards struct{ card telegramstate.Card }

func (c cards) Load(context.Context, domain.SessionID) (telegramstate.Card, bool, error) {
	return c.card, true, nil
}

type cardMap map[domain.SessionID]telegramstate.Card

func (cards cardMap) Load(_ context.Context, id domain.SessionID) (telegramstate.Card, bool, error) {
	card, ok := cards[id]
	return card, ok, nil
}

func update(actor, chat, message int64) coordinator.Update {
	return coordinator.Update{Kind: coordinator.UpdateCallback, ActorID: actor, ConversationID: chat, ConversationKind: "private", CallbackQueryID: "query", SourceMessageID: message}
}
func callback() telegrambridge.Callback { return telegrambridge.Callback{SessionID: string(sessionID)} }
func card() telegramstate.Card {
	return telegramstate.Card{SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
}

func TestValidateCallbackBindsOwnerPrivateChatAndCarrier(t *testing.T) {
	ctx := context.Background()
	for name, in := range map[string]struct {
		update coordinator.Update
		want   error
	}{
		"wrong owner":   {update(8, 42, 99), telegrampipeline.ErrNotOwner},
		"wrong chat":    {update(7, 41, 99), telegrampipeline.ErrNotPrivate},
		"stale message": {update(7, 42, 100), telegrampipeline.ErrStaleCallback},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := telegrampipeline.ValidateCallback(ctx, telegrampipeline.CallbackInput{Update: in.update, Callback: callback()}, 7, 42, cards{card: card()})
			if !errors.Is(err, in.want) {
				t.Fatalf("error=%v want %v", err, in.want)
			}
		})
	}
	got, err := telegrampipeline.ValidateCallback(ctx, telegrampipeline.CallbackInput{Update: update(7, 42, 99), Callback: callback()}, 7, 42, cards{card: card()})
	if err != nil || got.Carrier.MessageID != 99 {
		t.Fatalf("valid callback = %#v, %v", got, err)
	}
}

func TestExecutePersistsUnknownAndNeverReplays(t *testing.T) {
	j := telegrampipeline.NewMemoryJournal()
	calls := 0
	_, err := telegrampipeline.Execute(context.Background(), j, telegrampipeline.Operation{ID: "op-1", Kind: "edit", UpdateID: 1}, func(context.Context) (int64, error) { calls++; return 0, errors.New("timeout") })
	if err == nil {
		t.Fatal("first call error=nil")
	}
	_, err = telegrampipeline.Execute(context.Background(), j, telegrampipeline.Operation{ID: "op-1", Kind: "edit", UpdateID: 1}, func(context.Context) (int64, error) { calls++; return 10, nil })
	if !errors.Is(err, telegrampipeline.ErrUnknownOperation) {
		t.Fatalf("replay error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
}

func TestExecuteConfirmedIsIdempotent(t *testing.T) {
	j := telegrampipeline.NewMemoryJournal()
	calls := 0
	op := telegrampipeline.Operation{ID: "op-2", Kind: "send", UpdateID: 2}
	got, err := telegrampipeline.Execute(context.Background(), j, op, func(context.Context) (int64, error) { calls++; return 11, nil })
	if err != nil || got != 11 {
		t.Fatalf("first=%d %v", got, err)
	}
	got, err = telegrampipeline.Execute(context.Background(), j, op, func(context.Context) (int64, error) { calls++; return 12, nil })
	if err != nil || got != 11 || calls != 1 {
		t.Fatalf("replay=%d %v calls=%d", got, err, calls)
	}
}

func TestAcceptCallbackClaimsCurrentPresentationOnceAndRejectsStaleButtons(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	presented, err := presenter.PresentKeyboardWithManifest(string(sessionID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionOptions},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	if err := registry.Replace(context.Background(), telegrampipeline.CallbackPresentation{
		SessionID: sessionID,
		Carrier:   card().Carrier,
		TokenIDs:  presented.TokenIDs,
		ExpiresAt: presented.ExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}

	callbackUpdate := update(7, 42, 99)
	callbackUpdate.ID = 101
	callbackUpdate.Text = presented.Markup.InlineKeyboard[0][0].CallbackData
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), callbackUpdate, 7, 42, cards{card: card()}, registry, presenter)
	if err != nil {
		t.Fatalf("AcceptCallback() error = %v", err)
	}
	if accepted.SessionID != sessionID || accepted.Action != telegramui.ActionOptions || accepted.Carrier != card().Carrier {
		t.Fatalf("accepted callback = %#v", accepted)
	}
	if _, err := telegrampipeline.AcceptCallback(context.Background(), callbackUpdate, 7, 42, cards{card: card()}, registry, presenter); !errors.Is(err, telegrampipeline.ErrReplayedCallback) {
		t.Fatalf("standalone exact replay error = %v, want ErrReplayedCallback", err)
	}

	replay := callbackUpdate
	replay.ID++
	replay.CallbackQueryID = "query-2"
	if _, err := telegrampipeline.AcceptCallback(context.Background(), replay, 7, 42, cards{card: card()}, registry, presenter); !errors.Is(err, telegrampipeline.ErrReplayedCallback) {
		t.Fatalf("replay error = %v, want ErrReplayedCallback", err)
	}

	replacement, err := presenter.PresentKeyboardWithManifest(string(sessionID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionScreen},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Replace(context.Background(), telegrampipeline.CallbackPresentation{
		SessionID: sessionID,
		Carrier:   card().Carrier,
		TokenIDs:  replacement.TokenIDs,
		ExpiresAt: replacement.ExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	stale := callbackUpdate
	stale.ID += 2
	stale.CallbackQueryID = "query-3"
	if _, err := telegrampipeline.AcceptCallback(context.Background(), stale, 7, 42, cards{card: card()}, registry, presenter); !errors.Is(err, telegrampipeline.ErrStaleCallback) {
		t.Fatalf("stale presentation error = %v, want ErrStaleCallback", err)
	}
}

func TestPlanAcceptedCallbackMapsEverySemanticAction(t *testing.T) {
	tests := []struct {
		action telegramui.Action
		target telegramui.ButtonTarget
		effect telegrampipeline.CallbackEffect
	}{
		{telegramui.ActionPagePrevious, telegramui.ButtonTarget{Page: 3}, telegrampipeline.EffectProjectPage},
		{telegramui.ActionPageLatest, telegramui.ButtonTarget{FollowLatest: true}, telegrampipeline.EffectProjectPage},
		{telegramui.ActionPageNext, telegramui.ButtonTarget{Page: 2}, telegrampipeline.EffectProjectPage},
		{telegramui.ActionStop, telegramui.ButtonTarget{}, telegrampipeline.EffectStopSession},
		{telegramui.ActionClose, telegramui.ButtonTarget{}, telegrampipeline.EffectCloseSession},
		{telegramui.ActionOptions, telegramui.ButtonTarget{}, telegrampipeline.EffectToggleOptions},
		{telegramui.ActionScreen, telegramui.ButtonTarget{}, telegrampipeline.EffectToggleGlobalScreen},
		{telegramui.ActionSelectSession, telegramui.ButtonTarget{}, telegrampipeline.EffectSelectSession},
		{telegramui.ActionResume, telegramui.ButtonTarget{}, telegrampipeline.EffectResumeSession},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
				UpdateID:  1,
				SessionID: sessionID,
				Carrier:   card().Carrier,
				Action:    test.action,
				Target:    test.target,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Effect != test.effect || plan.SessionID != sessionID || plan.Action != test.action || plan.Carrier != card().Carrier {
				t.Fatalf("plan = %#v", plan)
			}
		})
	}
}

func TestAcceptAndPlanSignedGlobalSurfaceWithoutPretendingItIsASessionCard(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	presented, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionMenuSettings},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 303}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	if err := telegrampipeline.BindPresentation(context.Background(), registry, carrier, presented); err != nil {
		t.Fatal(err)
	}
	update := coordinator.Update{
		ID: 303, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: presented.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-303", SourceMessageID: 303,
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), update, 7, 42, nil, registry, presenter)
	if err != nil {
		t.Fatalf("AcceptCallback(global) error = %v", err)
	}
	if accepted.SessionID != domain.SessionID(telegramui.GlobalSurfaceID) || accepted.Action != telegramui.ActionMenuSettings || accepted.Carrier != carrier {
		t.Fatalf("accepted global callback = %#v", accepted)
	}
	plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
	if err != nil || plan.Effect != telegrampipeline.EffectOpenSettings {
		t.Fatalf("global plan = %#v, %v", plan, err)
	}
}

func TestAcceptAndPlanStatusRecoveryKeepsExactDurableBinding(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 256)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	binding := statusrecovery.Binding{
		OperationID: "status:731", UpdateID: 731,
		Scope:   statusrecovery.Scope{Kind: statusrecovery.ScopeSession, SessionID: sessionID},
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 303}, Sequence: 731, Prepared: true, Edit: true,
	}
	presented, err := presenter.PresentStatusRecoveryKeyboardWithManifest(binding, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionStatusRecoveryAssumeDelivered}},
		{{Action: telegramui.ActionStatusRecoveryRetryPossibleDuplicate}},
		{{Action: telegramui.ActionStatusRecoveryCancel}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	if err := telegrampipeline.BindPresentation(context.Background(), registry, binding.Carrier, presented); err != nil {
		t.Fatal(err)
	}
	for index, want := range []struct {
		action telegramui.Action
		effect telegrampipeline.CallbackEffect
	}{
		{telegramui.ActionStatusRecoveryAssumeDelivered, telegrampipeline.EffectStatusRecoveryAssumeDelivered},
		{telegramui.ActionStatusRecoveryRetryPossibleDuplicate, telegrampipeline.EffectStatusRecoveryRetryPossibleDuplicate},
		{telegramui.ActionStatusRecoveryCancel, telegrampipeline.EffectStatusRecoveryCancel},
	} {
		accepted, acceptErr := telegrampipeline.AcceptCallbackForDurableOperation(context.Background(), coordinator.Update{
			ID: int64(900 + index), Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
			Text: presented.Markup.InlineKeyboard[index][0].CallbackData, CallbackQueryID: fmt.Sprintf("query-%d", index), SourceMessageID: 303,
		}, 7, 42, nil, registry, presenter)
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		plan, planErr := telegrampipeline.PlanAcceptedCallback(accepted)
		if planErr != nil || plan.Effect != want.effect || plan.Action != want.action || plan.StatusRecovery == nil ||
			plan.StatusRecovery.Decision != want.action || plan.StatusRecovery.Binding != binding {
			t.Fatalf("status recovery plan = %#v, %v", plan, planErr)
		}
	}
}

func TestGlobalArchiveResumeTargetsExactArchivedSessionWithoutActiveFallback(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	archivedID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	presented, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, []string{archivedID}, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionResume, Target: telegramui.ButtonTarget{SessionSlot: 1}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 404}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	if err := telegrampipeline.BindPresentation(context.Background(), registry, carrier, presented); err != nil {
		t.Fatal(err)
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), coordinator.Update{
		ID: 404, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: presented.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-404", SourceMessageID: 404,
	}, 7, 42, nil, registry, presenter)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
	if err != nil || plan.SessionID != domain.SessionID(archivedID) || plan.Effect != telegrampipeline.EffectResumeSession || plan.Carrier != carrier {
		t.Fatalf("archive resume plan = %#v, %v", plan, err)
	}
}

func TestInteractionCallbackBindsExactRequestSessionAndChoiceWithoutCardFallback(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	presented, err := presenter.PresentInteractionKeyboardWithManifest(string(sessionID), "opaque-provider-request", telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionInteractionChoice, Target: telegramui.ButtonTarget{InteractionChoice: 3}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 505}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	if err := telegrampipeline.BindPresentation(context.Background(), registry, carrier, presented); err != nil {
		t.Fatal(err)
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), coordinator.Update{
		ID: 505, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private",
		Text: presented.Markup.InlineKeyboard[0][0].CallbackData, CallbackQueryID: "query-505", SourceMessageID: 505,
	}, 7, 42, nil, registry, presenter)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SessionID != sessionID || plan.Effect != telegrampipeline.EffectInteractionChoice || plan.Interaction == nil ||
		plan.Interaction.RequestID != "opaque-provider-request" || plan.Interaction.ChoiceIndex != 3 {
		t.Fatalf("interaction plan = %#v", plan)
	}
}

func TestAcceptSessionSelectionBindsTargetToOwningCardCarrier(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ownerSessionID := domain.SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	targetSessionID := domain.SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	presented, err := presenter.PresentKeyboardWithManifest(string(ownerSessionID), []string{string(targetSessionID)}, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 1}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ownerCard := card()
	ownerCard.SessionID = ownerSessionID
	ownerCard.Carrier = telegramstate.Carrier{ChatID: 42, MessageID: 77}
	targetCard := card()
	targetCard.SessionID = targetSessionID
	targetCard.Carrier = telegramstate.Carrier{ChatID: 42, MessageID: 88}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	if err := registry.Replace(context.Background(), telegrampipeline.CallbackPresentation{
		SessionID: ownerSessionID,
		Carrier:   ownerCard.Carrier,
		TokenIDs:  presented.TokenIDs,
		ExpiresAt: presented.ExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	callbackUpdate := update(7, 42, 77)
	callbackUpdate.ID = 200
	callbackUpdate.Text = presented.Markup.InlineKeyboard[0][0].CallbackData
	accepted, err := telegrampipeline.AcceptCallback(
		context.Background(),
		callbackUpdate,
		7,
		42,
		cardMap{ownerSessionID: ownerCard, targetSessionID: targetCard},
		registry,
		presenter,
	)
	if err != nil {
		t.Fatalf("AcceptCallback(select session) error = %v", err)
	}
	if accepted.SessionID != targetSessionID || accepted.Action != telegramui.ActionSelectSession || accepted.Carrier != ownerCard.Carrier {
		t.Fatalf("accepted selection = %#v", accepted)
	}
}

func TestCallbackClaimIsAtomic(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	presentation := telegrampipeline.CallbackPresentation{
		SessionID: sessionID,
		Carrier:   card().Carrier,
		TokenIDs:  []string{"opaque-token-id"},
		ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	claim := telegrampipeline.CallbackClaim{
		SessionID:       sessionID,
		Carrier:         card().Carrier,
		TokenID:         "opaque-token-id",
		ExpiresAt:       presentation.ExpiresAt,
		UpdateID:        1,
		CallbackQueryID: "query",
	}
	const contenders = 16
	var wg sync.WaitGroup
	results := make(chan telegrampipeline.ClaimOutcome, contenders)
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			candidate := claim
			candidate.UpdateID += int64(index)
			candidate.CallbackQueryID += string(rune('a' + index))
			result, err := registry.Claim(context.Background(), candidate)
			if err != nil {
				t.Errorf("Claim() error = %v", err)
				return
			}
			results <- result.Outcome
		}(index)
	}
	wg.Wait()
	close(results)
	accepted, replayed := 0, 0
	for result := range results {
		switch result {
		case telegrampipeline.ClaimAccepted:
			accepted++
		case telegrampipeline.ClaimReplayed:
			replayed++
		}
	}
	if accepted != 1 || replayed != contenders-1 {
		t.Fatalf("claim outcomes: accepted=%d replayed=%d", accepted, replayed)
	}
}

func TestCallbackClaimRecoversOnlyTheExactPersistedTelegramUpdate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	presentation := telegrampipeline.CallbackPresentation{
		SessionID: sessionID,
		Carrier:   card().Carrier,
		TokenIDs:  []string{"opaque-token-id"},
		ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), presentation); err != nil {
		t.Fatal(err)
	}
	claim := telegrampipeline.CallbackClaim{
		SessionID:       sessionID,
		Carrier:         card().Carrier,
		TokenID:         "opaque-token-id",
		ExpiresAt:       presentation.ExpiresAt,
		UpdateID:        101,
		CallbackQueryID: "query-101",
	}
	first, err := registry.Claim(context.Background(), claim)
	if err != nil || first.Outcome != telegrampipeline.ClaimAccepted {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	recovered, err := registry.Claim(context.Background(), claim)
	if err != nil || recovered.Outcome != telegrampipeline.ClaimRecovered {
		t.Fatalf("exact recovery = %#v, %v", recovered, err)
	}
	different := claim
	different.UpdateID++
	different.CallbackQueryID = "query-102"
	replayed, err := registry.Claim(context.Background(), different)
	if err != nil || replayed.Outcome != telegrampipeline.ClaimReplayed {
		t.Fatalf("different update replay = %#v, %v", replayed, err)
	}
}

func TestAcceptCallbackUsesExactAcceptedTurnRecoveryBindingWithoutCardFallback(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	binding := telegrambridge.AcceptedTurnRecoveryBinding{SessionID: sessionID, MessageID: "telegram-update:301", BindingGeneration: 7}
	presented, err := presenter.PresentAcceptedTurnRecoveryKeyboardWithManifest(binding, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionAcceptedTurnRetryPossibleDuplicate},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 909}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, carrier, presented); err != nil {
		t.Fatal(err)
	}
	callbackUpdate := update(7, 42, 909)
	callbackUpdate.ID = 301
	callbackUpdate.CallbackQueryID = "accepted-turn-301"
	callbackUpdate.Text = presented.Markup.InlineKeyboard[0][0].CallbackData
	accepted, err := telegrampipeline.AcceptCallbackForDurableOperation(context.Background(), callbackUpdate, 7, 42, nil, registry, presenter)
	if err != nil {
		t.Fatal(err)
	}
	want := &telegrampipeline.AcceptedTurnRecoveryBinding{SessionID: sessionID, MessageID: "telegram-update:301", BindingGeneration: 7}
	if accepted.SessionID != sessionID || accepted.Action != telegramui.ActionAcceptedTurnRetryPossibleDuplicate ||
		accepted.AcceptedTurnRecovery == nil || *accepted.AcceptedTurnRecovery != *want {
		t.Fatalf("accepted-turn recovery = %#v, want binding %#v", accepted, want)
	}
	plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Effect != telegrampipeline.EffectAcceptedTurnRetryPossibleDuplicate || plan.AcceptedTurnRecovery == nil ||
		plan.AcceptedTurnRecovery.SessionID != sessionID || plan.AcceptedTurnRecovery.MessageID != "telegram-update:301" ||
		plan.AcceptedTurnRecovery.BindingGeneration != 7 || plan.AcceptedTurnRecovery.Decision != telegramui.ActionAcceptedTurnRetryPossibleDuplicate {
		t.Fatalf("accepted-turn recovery plan = %#v", plan)
	}
	replay := callbackUpdate
	replay.ID++
	replay.CallbackQueryID = "accepted-turn-replay"
	if _, err := telegrampipeline.AcceptCallbackForDurableOperation(context.Background(), replay, 7, 42, nil, registry, presenter); !errors.Is(err, telegrampipeline.ErrReplayedCallback) {
		t.Fatalf("second recovery use error = %v, want replay", err)
	}
}

func TestPlanAcceptedTurnRecoveryExposesEveryExplicitDecision(t *testing.T) {
	t.Parallel()
	binding := &telegrampipeline.AcceptedTurnRecoveryBinding{
		SessionID: sessionID, MessageID: "telegram-update:301", BindingGeneration: 7,
	}
	for action, effect := range map[telegramui.Action]telegrampipeline.CallbackEffect{
		telegramui.ActionAcceptedTurnAssumeCompleted:        telegrampipeline.EffectAcceptedTurnAssumeCompleted,
		telegramui.ActionAcceptedTurnRetryPossibleDuplicate: telegrampipeline.EffectAcceptedTurnRetryPossibleDuplicate,
		telegramui.ActionAcceptedTurnCancel:                 telegrampipeline.EffectAcceptedTurnCancel,
	} {
		plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
			UpdateID: 301, SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 909},
			Action: action, AcceptedTurnRecovery: binding,
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Effect != effect || plan.AcceptedTurnRecovery == nil || plan.AcceptedTurnRecovery.Decision != action {
			t.Fatalf("plan for %q = %#v", action, plan)
		}
	}
}

func TestBindPresentationRequiresConfirmedCarrierAndPublishesManifest(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	presentation := telegrambridge.KeyboardPresentation{
		SessionID: string(sessionID),
		TokenIDs:  []string{"token"},
		ExpiresAt: now.Add(time.Minute),
	}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, telegramstate.Carrier{}, presentation); err == nil {
		t.Fatal("BindPresentation() accepted carrier without confirmed receipt")
	}
	if err := telegrampipeline.BindPresentation(context.Background(), registry, card().Carrier, presentation); err != nil {
		t.Fatalf("BindPresentation() error = %v", err)
	}
	result, err := registry.Claim(context.Background(), telegrampipeline.CallbackClaim{
		SessionID:       sessionID,
		Carrier:         card().Carrier,
		TokenID:         "token",
		ExpiresAt:       presentation.ExpiresAt,
		UpdateID:        1,
		CallbackQueryID: "query",
	})
	if err != nil || result.Outcome != telegrampipeline.ClaimAccepted || result.PresentationSessionID != sessionID {
		t.Fatalf("bound presentation claim = %#v, %v", result, err)
	}
}

func TestReplacingCarrierAcrossGlobalAndSessionSurfacesInvalidatesOldKeyboard(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 99}
	global := telegrampipeline.CallbackPresentation{
		SessionID: domain.SessionID(telegramui.GlobalSurfaceID), Carrier: carrier,
		TokenIDs: []string{"global-token"}, ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), global); err != nil {
		t.Fatal(err)
	}
	session := telegrampipeline.CallbackPresentation{
		SessionID: sessionID, Carrier: carrier,
		TokenIDs: []string{"session-token"}, ExpiresAt: now.Add(time.Minute),
	}
	if err := registry.Replace(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Claim(context.Background(), telegrampipeline.CallbackClaim{
		SessionID: global.SessionID, Carrier: carrier, TokenID: "global-token",
		ExpiresAt: global.ExpiresAt, UpdateID: 99, CallbackQueryID: "old-global",
	})
	if err != nil || result.Outcome != telegrampipeline.ClaimStale {
		t.Fatalf("old carrier keyboard claim = %#v, %v want stale", result, err)
	}
}

func TestStateCardStoreAdaptsDurableUIState(t *testing.T) {
	stateStore := telegramstate.NewMemoryStore()
	if err := stateStore.Update(context.Background(), func(state *telegramstate.State) error {
		return state.SetCard(card())
	}); err != nil {
		t.Fatal(err)
	}
	store, err := telegrampipeline.NewStateCardStore(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Load(context.Background(), sessionID)
	if err != nil || !ok || got.Carrier != card().Carrier {
		t.Fatalf("Load() = %#v, %t, %v", got, ok, err)
	}
}
