package telegramflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestCardCarrierRejectionIsCorrelatedInPhysicalLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := observability.NewTelegramFlowObserver(logger)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := newPresenter(t, now)
	state := telegramstate.NewMemoryStore()
	view := telegramui.PageView{Page: 2, Pages: 2, FollowLatest: true}
	prepared, err := telegramflow.PrepareCardRefresh("refresh-private", flowSessionID, 42, 99,
		telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "PRIVATE-FIRST"}, {Content: "PRIVATE-SECOND"}},
			View: view, Keyboard: telegramui.CardKeyboardInput{View: view}}, "PRIVATE-HEADER", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: state,
		MessageUI: semanticMessageHandler{}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{receipt: coordinator.Receipt{MessageID: 99}}, Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); err != nil {
		t.Fatal(err)
	}
	if err := state.Update(ctx, func(s *telegramstate.State) error {
		card, _ := s.Card(flowSessionID)
		card.Carrier.MessageID = 101
		return s.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	button := (*prepared.Keyboard)[0][0].CallbackData
	decision, err := handler.Handle(ctx, coordinator.Update{ID: 50, Kind: coordinator.UpdateCallback, ActorID: 7,
		ConversationID: 42, ConversationKind: "private", SourceMessageID: 99, Text: button, CallbackQueryID: "PRIVATE-QUERY"})
	if err != nil || decision.Kind != coordinator.DecisionSkip {
		t.Fatalf("rejection changed: %v %v", decision.Kind, err)
	}
	observer.Close()
	records, err := logger.Read(safelog.Detailed)
	if err != nil {
		t.Fatal(err)
	}
	var bound, rejected safelog.Event
	for _, record := range records {
		if record.Fields["stage"] == "state.commit" {
			bound = record
		}
		if record.Fields["stage"] == "callback.discard" {
			rejected = record
		}
	}
	if rejected.ErrorCategory != "card_carrier_mismatch" || rejected.Fields["callback_ref"] == "" ||
		rejected.Fields["card_ref"] == rejected.Fields["expected_card_ref"] {
		t.Fatalf("missing physical rejection diagnosis: %+v", rejected)
	}
	if !strings.Contains(bound.Fields["button_refs"], rejected.Fields["callback_ref"]) || bound.Fields["card_ref"] != rejected.Fields["card_ref"] {
		t.Fatalf("cannot link rejected button to delivered card: bound=%+v rejected=%+v", bound, rejected)
	}
	data, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{button, "PRIVATE-FIRST", "PRIVATE-SECOND", "PRIVATE-HEADER", "PRIVATE-QUERY", "refresh-private", string(flowSessionID)} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("raw private data leaked: %q", forbidden)
		}
	}
}
