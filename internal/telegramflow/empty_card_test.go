package telegramflow

import (
	"context"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestFirstCardDeliveryPreservesEmptyEvidenceAndDoesNotStorePlaceholder(t *testing.T) {
	ctx := context.Background()
	id := domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	store := telegramstate.NewMemoryStore()
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		return state.SetCard(telegramstate.Card{SessionID: id, EmptyCloseEligible: true, Page: telegramstate.Page{Current: 1, Total: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	output := CardOutput{SessionID: id, MakeActive: true, Projection: telegramui.CarrierProjection{Card: telegramui.ProjectedCard{
		Pages: []telegramui.ContentPage{{Content: "Пока нет сообщений CLI."}},
		View:  telegramui.PageView{Page: 1, Pages: 1},
	}}}
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		return commitCard(state, output, telegramstate.Carrier{ChatID: 42, MessageID: 10})
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load(ctx)
	card, _ := state.Card(id)
	if !card.EmptyCloseEligible || len(card.History) != 0 {
		t.Fatalf("presentation destroyed empty-session evidence: %#v", card)
	}
}
