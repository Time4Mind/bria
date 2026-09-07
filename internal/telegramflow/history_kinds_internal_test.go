package telegramflow

import (
	"context"
	"reflect"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestCommitCardPreservesTypedHistoryMetadata(t *testing.T) {
	ctx := context.Background()
	id := domain.SessionID("123e4567-e89b-12d3-a456-426614174099")
	store := telegramstate.NewMemoryStore()
	wantHistory := []string{"ordinary", "technical"}
	wantKeys := []string{"telegram-update:1", ""}
	wantKinds := []string{"", "tool"}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		return state.SetCard(telegramstate.Card{
			SessionID:    id,
			Carrier:      telegramstate.Carrier{ChatID: 42, MessageID: 7},
			Page:         telegramstate.Page{Current: 1, Total: 1, FollowLatest: true},
			History:      wantHistory,
			HistoryKeys:  wantKeys,
			HistoryKinds: wantKinds,
		})
	}); err != nil {
		t.Fatal(err)
	}
	output := CardOutput{SessionID: id, Projection: telegramui.CarrierProjection{Card: telegramui.ProjectedCard{
		View: telegramui.PageView{Page: 1, Pages: 1, FollowLatest: true},
	}}}
	carrier := telegramstate.Carrier{ChatID: 42, MessageID: 8}
	if err := commitCard(ctx, store, output, carrier); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, ok := state.Card(id)
	if !ok || card.Carrier != carrier || !reflect.DeepEqual(card.History, wantHistory) ||
		!reflect.DeepEqual(card.HistoryKeys, wantKeys) || !reflect.DeepEqual(card.HistoryKinds, wantKinds) {
		t.Fatalf("committed card = %#v, found=%t", card, ok)
	}
}
