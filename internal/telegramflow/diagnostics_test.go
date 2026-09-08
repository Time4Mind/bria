package telegramflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestFailedCardEditRetainsTargetInTrace(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	trace := &traceObserver{}
	presenter := newPresenter(t, now)
	wantErr := errors.New("network failure")
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          telegramstate.NewMemoryStore(), MessageUI: semanticMessageHandler{}, Callbacks: &globalCallbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{err: wantErr}, Observer: trace,
	})
	if err != nil {
		t.Fatal(err)
	}
	view := telegramui.PageView{Page: 1, Pages: 1, FollowLatest: true}
	prepared, err := telegramflow.PrepareCardRefresh("failed-refresh", flowSessionID, 42, 99,
		telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "private"}}, View: view,
			Keyboard: telegramui.CardKeyboardInput{View: view}}, "", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Register(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.EditStatusWithKeyboard(ctx, prepared.OperationID, prepared.Status, prepared.Keyboard); !errors.Is(err, wantErr) {
		t.Fatalf("send error changed: %v", err)
	}
	for _, event := range trace.events {
		if event.Stage == "state.commit" {
			t.Fatal("failed send claimed a state commit")
		}
		if event.Stage == "transport.edit" {
			if event.CarrierID != 99 || event.ChatID != 42 || event.Reason != "operation_failed" || event.Result != "failed" {
				t.Fatalf("failed edit lost its target: %+v", event)
			}
			return
		}
	}
	t.Fatal("failed transport event missing")
}

func TestRejectedCallbackTraceExplainsDiscardWithoutPayload(t *testing.T) {
	for _, reason := range []string{"token_invalid", "presentation_missing", "presentation_replayed"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0).UTC()
			presenter := newPresenter(t, now)
			registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
			trace := &traceObserver{}
			callbacks := &globalCallbackExecutor{}
			handler, _, err := telegramflow.New(telegramflow.Config{
				OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
				UIState: telegramstate.NewMemoryStore(), MessageUI: semanticMessageHandler{}, Callbacks: callbacks,
				Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{}, Observer: trace,
			})
			if err != nil {
				t.Fatal(err)
			}
			presentation, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil,
				telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionMenuSettings}}}})
			if err != nil {
				t.Fatal(err)
			}
			update := coordinator.Update{ID: 20, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42,
				ConversationKind: "private", SourceMessageID: 99, CallbackQueryID: "query-private",
				Text: presentation.Markup.InlineKeyboard[0][0].CallbackData}
			if reason == "token_invalid" {
				update.Text = "invalid-private-payload"
			}
			if reason == "presentation_replayed" {
				if err := telegrampipeline.BindPresentation(ctx, registry, telegramstate.Carrier{ChatID: 42, MessageID: 99}, presentation); err != nil {
					t.Fatal(err)
				}
				if _, err := handler.Handle(ctx, update); err != nil {
					t.Fatal(err)
				}
				update.ID++
				update.CallbackQueryID = "query-private-second"
			}
			trace.events = nil
			callsBefore := callbacks.calls
			decision, err := handler.Handle(ctx, update)
			if err != nil || decision.Kind != coordinator.DecisionSkip || callbacks.calls != callsBefore {
				t.Fatalf("diagnostics changed rejection behavior: %v %v", decision.Kind, err)
			}
			var accept, discard *telegramflow.TraceEvent
			for i := range trace.events {
				event := &trace.events[i]
				if event.Stage == "callback.accept" {
					accept = event
				}
				if event.Stage == "callback.discard" {
					discard = event
				}
			}
			if accept == nil || accept.Reason != reason || accept.CallbackID == "" || accept.Time.IsZero() {
				t.Fatalf("missing typed rejection metadata for %s: %+v", reason, accept)
			}
			if discard == nil || discard.Result != "skipped" || discard.Reason != reason || discard.CallbackID != accept.CallbackID {
				t.Fatalf("silent discard was not explained: %+v", discard)
			}
			encoded, err := json.Marshal(trace.events)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), update.Text) || strings.Contains(string(encoded), update.CallbackQueryID) {
				t.Fatal("callback payload/query identity leaked into diagnostic event")
			}
		})
	}
}

func TestCardTraceCorrelatesVisibleKeyboardAndBinding(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	trace := &traceObserver{}
	handler, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, now),
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          telegramstate.NewMemoryStore(), MessageUI: semanticMessageHandler{}, Callbacks: &globalCallbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{receipt: coordinator.Receipt{MessageID: 99}}, Observer: trace,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := handler.Handle(ctx, coordinator.Update{ID: 1, Kind: coordinator.UpdateMessage, ActorID: 7,
		ConversationID: 42, ConversationKind: "private", Text: "/menu"})
	if err != nil {
		t.Fatal(err)
	}
	trace.events = nil
	if _, err := outbound.SendStatusWithKeyboard(ctx, "status:1", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	want := []string{"card.send_started", "transport.send", "card.finalize_started", "state.commit"}
	var got []string
	for _, event := range trace.events {
		if event.Stage == want[len(got)] {
			if event.ChatID != 42 || event.PresentationID == "" || len(event.ButtonIDs) != 2 || event.Time.IsZero() {
				t.Fatalf("missing card correlation metadata: %+v", event)
			}
			if event.Stage != "card.send_started" && event.CarrierID != 99 {
				t.Fatalf("missing confirmed carrier: %+v", event)
			}
			got = append(got, event.Stage)
			if len(got) == len(want) {
				break
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("card lifecycle = %v, want %v", got, want)
	}
}
