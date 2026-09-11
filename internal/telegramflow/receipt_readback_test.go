package telegramflow_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type advanceAfterCommitStore struct {
	telegramstate.Store
	next func(*telegramstate.State) error
}

func (s *advanceAfterCommitStore) Update(ctx context.Context, update func(*telegramstate.State) error) error {
	if err := s.Store.Update(ctx, update); err != nil {
		return err
	}
	return s.Store.Update(ctx, s.next)
}

func TestKnownReceiptSurvivesConcurrentHistoryOrNewerPublication(t *testing.T) {
	for _, mode := range []string{"history", "newer-carrier", "same-carrier-view"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0).UTC()
			presenter := newPresenter(t, now)
			store := &advanceAfterCommitStore{Store: telegramstate.NewMemoryStore(), next: func(state *telegramstate.State) error {
				card, _ := state.Card(flowSessionID)
				card.History = append(card.History, "concurrent history")
				if mode == "newer-carrier" {
					card.Carrier.MessageID = 5515
				}
				if mode != "history" {
					card.LastPresentationOperation = "newer-operation"
				}
				return state.SetCard(card)
			}}
			_, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: store, Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{receipt: coordinator.Receipt{MessageID: 5514}}})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := telegramflow.PrepareCompletion("new-card", flowSessionID, 42, true, "", telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "answer", Anchors: []string{"answer"}}}, View: telegramui.PageView{Page: 1, Pages: 1, Anchor: "answer"}}, false, nil, presenter)
			if err != nil {
				t.Fatal(err)
			}
			if err := outbound.Register(prepared); err != nil {
				t.Fatal(err)
			}
			if got, err := outbound.SendStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil || got.MessageID != 5514 {
				t.Fatalf("known receipt rejected after legitimate advance: %+v %v", got, err)
			}
			state, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			card, _ := state.Card(flowSessionID)
			if len(card.History) != 2 || card.History[1] != "concurrent history" {
				t.Fatalf("concurrent history lost: %+v", card)
			}
			if mode == "newer-carrier" && card.Carrier.MessageID != 5515 {
				t.Fatal("newer publication rolled back")
			}
		})
	}
}
