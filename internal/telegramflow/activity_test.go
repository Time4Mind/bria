package telegramflow_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
)

type activityMessageHandler struct{ before func(coordinator.Update) }

func (h activityMessageHandler) Handle(_ context.Context, update coordinator.Update) (coordinator.Decision, error) {
	if h.before != nil {
		h.before(update)
	}
	return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
}

func newActivityHandler(t *testing.T, hook func(coordinator.Update), before func(coordinator.Update)) *telegramflow.Handler {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	handler, _, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: newPresenter(t, now),
		CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
		UIState:          telegramstate.NewMemoryStore(), Messages: activityMessageHandler{before}, Callbacks: &callbackExecutor{},
		Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: &sender{}, OnUserAction: hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func activityUpdate() coordinator.Update {
	return coordinator.Update{ID: 10, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", SourceMessageID: 100, Text: "synthetic"}
}

func TestAuthenticatedUserActionHookBeforePayloadMatrix(t *testing.T) {
	cases := []struct {
		name   string
		change func(*coordinator.Update)
		want   bool
	}{
		{"text", func(*coordinator.Update) {}, true},
		{"voice", func(u *coordinator.Update) { u.Text = ""; u.MediaKind = "voice" }, true},
		{"photo", func(u *coordinator.Update) { u.Text = ""; u.MediaKind = "photo" }, true},
		{"document", func(u *coordinator.Update) { u.Text = ""; u.MediaKind = "document" }, true},
		{"video", func(u *coordinator.Update) { u.Text = ""; u.MediaKind = "video" }, true},
		{"caption", func(u *coordinator.Update) { u.Text = ""; u.Caption = "synthetic caption"; u.MediaKind = "photo" }, true},
		{"unsupported_media", func(u *coordinator.Update) { u.Text = ""; u.MediaKind = "unsupported" }, true},
		{"empty_message", func(u *coordinator.Update) { u.Text = "" }, true},
		{"menu_command", func(u *coordinator.Update) { u.Text = "/menu" }, true},
		{"session_command", func(u *coordinator.Update) { u.Text = "/sessions" }, true},
		{"native_command", func(u *coordinator.Update) { u.Text = "/model" }, true},
		{"unauthorized_actor", func(u *coordinator.Update) { u.ActorID = 8 }, false},
		{"wrong_chat", func(u *coordinator.Update) { u.ConversationID = 43 }, false},
		{"group", func(u *coordinator.Update) { u.ConversationKind = "group" }, false},
		{"zero_id", func(u *coordinator.Update) { u.ID = 0 }, false},
		{"negative_id", func(u *coordinator.Update) { u.ID = -1 }, false},
		{"background_unsupported", func(u *coordinator.Update) { u.Kind = coordinator.UpdateUnsupported }, false},
	}
	for _, name := range []string{"native_key", "settings", "page_previous", "recovery", "session_select", "expired_button", "malformed_button"} {
		cases = append(cases, struct {
			name   string
			change func(*coordinator.Update)
			want   bool
		}{name, func(u *coordinator.Update) {
			u.Kind = coordinator.UpdateCallback
			u.CallbackQueryID = "synthetic-query"
			u.Text = name
		}, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			update := activityUpdate()
			tc.change(&update)
			var seen []coordinator.Update
			handler := newActivityHandler(t, func(u coordinator.Update) { seen = append(seen, u) }, func(coordinator.Update) {
				if (len(seen) == 1) != tc.want {
					t.Errorf("payload began before authenticated activity: got=%d want=%v", len(seen), tc.want)
				}
			})
			_, _ = handler.Handle(context.Background(), update)
			if (len(seen) == 1) != tc.want {
				t.Fatalf("activity hook: got=%d want=%v", len(seen), tc.want)
			}
			if len(seen) > 0 && (seen[0].ID != update.ID || seen[0].ActorID != update.ActorID || seen[0].ConversationID != update.ConversationID) {
				t.Fatal("activity identity changed")
			}
		})
	}
}

func TestUserActivityHookOptionalAndBackgroundRecoveryDoesNotInvokeIt(t *testing.T) {
	handler := newActivityHandler(t, nil, nil)
	if _, err := handler.Handle(context.Background(), activityUpdate()); err != nil {
		t.Fatal(err)
	}
	var count int
	handler = newActivityHandler(t, func(coordinator.Update) { count++ }, nil)
	if _, err := handler.ListUnknown(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	update := activityUpdate()
	update.Kind = coordinator.UpdateCallback
	update.CallbackQueryID = "synthetic-query"
	_, _, _ = handler.PrepareUnknownRecovery(context.Background(), update)
	if count != 0 {
		t.Fatal("background recovery reset user activity")
	}
}
