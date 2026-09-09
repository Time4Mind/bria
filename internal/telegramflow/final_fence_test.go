package telegramflow_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type finalFenceCommitStore struct {
	telegramstate.Store
	fail bool
}

func TestLateFinalCommitPreservesNewerActiveSelection(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	presenter := newPresenter(t, now)
	state := telegramstate.NewMemoryStore()
	const selected domain.SessionID = "22222222-2222-4222-9222-222222222222"
	if err := state.Update(ctx, func(s *telegramstate.State) error {
		for _, id := range []domain.SessionID{flowSessionID, selected} {
			if err := s.SetCard(telegramstate.Card{SessionID: id, Page: telegramstate.Page{Current: 1, Total: 1}}); err != nil {
				return err
			}
		}
		s.ActiveSession = flowSessionID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, out, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: state,
		Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{receipt: coordinator.Receipt{MessageID: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := telegramflow.PrepareCompletion("A:final", flowSessionID, 42, true, telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "answer", Anchors: []string{"answer"}, FinalStart: true}},
		View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "answer"}}, false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Card.FinalOperationID = "A:final"
	if err := out.Register(prepared); err != nil {
		t.Fatal(err)
	}
	// User selection is committed after final preparation, before its receipt.
	if err := state.Update(ctx, func(s *telegramstate.State) error { s.ActiveSession = selected; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := out.SendStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatal(err)
	}
	got, err := state.Load(ctx)
	if err != nil || got.ActiveSession != selected || got.Cards[flowSessionID].Carrier.MessageID != 100 {
		t.Fatalf("late final must commit its carrier without replacing newer selection: active=%s err=%v", got.ActiveSession, err)
	}
}

func (s finalFenceCommitStore) Update(ctx context.Context, update func(*telegramstate.State) error) error {
	if s.fail {
		return errors.New("carrier commit failed")
	}
	return s.Store.Update(ctx, update)
}

func TestFinalCarrierCommitClearsOnlyItsExactPendingOperation(t *testing.T) {
	for _, test := range []struct {
		name, finalID string
		failure       error
		commitFailure bool
		want          []string
	}{
		{"final A", "A:final", nil, false, []string{"B:final"}},
		{"ordinary card", "", nil, false, []string{"A:final", "B:final"}},
		{"unknown final", "different:final", nil, false, []string{"A:final", "B:final"}},
		{"send failed", "A:final", errors.New("transport failed"), false, []string{"A:final", "B:final"}},
		{"carrier commit failed", "A:final", nil, true, []string{"A:final", "B:final"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0)
			presenter := newPresenter(t, now)
			state := telegramstate.NewMemoryStore()
			if err := state.Update(ctx, func(s *telegramstate.State) error {
				return s.SetCard(telegramstate.Card{SessionID: flowSessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99},
					Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}, PendingFinalOperations: []string{"A:final", "B:final"}})
			}); err != nil {
				t.Fatal(err)
			}
			base := &sender{receipt: coordinator.Receipt{MessageID: 100}, err: test.failure}
			_, out, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
				CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: finalFenceCommitStore{Store: state, fail: test.commitFailure},
				Messages: &messageHandler{}, Callbacks: &callbackExecutor{}, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: base})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := telegramflow.PrepareCompletion("A:final", flowSessionID, 42, true, telegramui.CardProjectionInput{
				Pages: []telegramui.ContentPage{{Content: "answer", Anchors: []string{"answer"}, FinalStart: true}},
				View:  telegramui.PageView{Page: 1, Pages: 1, Anchor: "answer", FollowLatest: true}}, false, nil, presenter)
			if err != nil {
				t.Fatal(err)
			}
			prepared.Card.FinalOperationID = test.finalID
			if err := out.Register(prepared); err != nil {
				t.Fatal(err)
			}
			_, err = out.SendStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard)
			if (err != nil) != (test.failure != nil || test.commitFailure) {
				t.Fatalf("unexpected send result: %v", err)
			}
			got, err := state.Load(ctx)
			if err != nil || !reflect.DeepEqual(got.Cards[flowSessionID].PendingFinalOperations, test.want) {
				t.Fatalf("pending=%v want=%v err=%v", got.Cards[flowSessionID].PendingFinalOperations, test.want, err)
			}
			if (test.failure != nil || test.commitFailure) && got.Cards[flowSessionID].Carrier.MessageID != 99 {
				t.Fatal("uncommitted final replaced stored old carrier")
			}
		})
	}
}
