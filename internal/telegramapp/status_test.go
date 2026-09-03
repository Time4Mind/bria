package telegramapp_test

import (
	"context"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/telegramapp"
)

func TestStatusHandlerReturnsFixedSafeReadinessForExactOwnerPrivateChat(t *testing.T) {
	t.Parallel()

	handler := mustStatusHandler(t, 42, 42)
	for _, text := range []string{"/status", " \t/status\r\n"} {
		decision, err := handler.Handle(context.Background(), coordinator.Update{
			ID:               101,
			Kind:             coordinator.UpdateMessage,
			ActorID:          42,
			ConversationID:   42,
			ConversationKind: "private",
			Text:             text,
		})
		if err != nil {
			t.Fatalf("Handle(%q) error = %v", text, err)
		}
		want := coordinator.Decision{
			Kind: coordinator.DecisionStatus,
			Status: coordinator.Status{
				ConversationID: 42,
				Text:           "Bria работает и готова принимать команды.",
			},
		}
		if decision != want {
			t.Fatalf("Handle(%q) = %#v, want %#v", text, decision, want)
		}
	}
}

func TestStatusHandlerSilentlySkipsEveryUnauthorizedGateMismatch(t *testing.T) {
	t.Parallel()

	handler := mustStatusHandler(t, 42, 42)
	tests := []struct {
		name   string
		update coordinator.Update
	}{
		{
			name: "different actor",
			update: coordinator.Update{
				Kind: coordinator.UpdateMessage, ActorID: 43, ConversationID: 42, ConversationKind: "private", Text: "/status",
			},
		},
		{
			name: "different private conversation",
			update: coordinator.Update{
				Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 43, ConversationKind: "private", Text: "/status",
			},
		},
		{
			name: "group",
			update: coordinator.Update{
				Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "group", Text: "/status",
			},
		},
		{
			name: "channel",
			update: coordinator.Update{
				Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "channel", Text: "/status",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := handler.Handle(context.Background(), test.update)
			if err != nil {
				t.Fatalf("Handle() error = %v, want silent skip", err)
			}
			if want := (coordinator.Decision{Kind: coordinator.DecisionSkip}); decision != want {
				t.Fatalf("Handle() = %#v, want %#v", decision, want)
			}
		})
	}
}

func TestStatusHandlerBlocksEveryOtherAuthorizedCurrentUpdate(t *testing.T) {
	t.Parallel()

	handler := mustStatusHandler(t, 42, 42)
	for _, text := range []string{
		"",
		"hello",
		"/start",
		"/status now",
		"/status@my_bria_bot",
		"/Status",
	} {
		decision, err := handler.Handle(context.Background(), coordinator.Update{
			ID:               101,
			Kind:             coordinator.UpdateMessage,
			ActorID:          42,
			ConversationID:   42,
			ConversationKind: "private",
			Text:             text,
		})
		if err != nil {
			t.Fatalf("Handle(%q) error = %v", text, err)
		}
		want := coordinator.Decision{
			Kind:        coordinator.DecisionBlock,
			BlockReason: "authorized update is not supported",
		}
		if decision != want {
			t.Fatalf("Handle(%q) = %#v, want %#v", text, decision, want)
		}
	}
}

func TestStatusHandlerDoesNotTreatCallbackDataAsStatusCommand(t *testing.T) {
	t.Parallel()

	handler := mustStatusHandler(t, 42, 42)
	decision, err := handler.Handle(context.Background(), coordinator.Update{
		ID:               101,
		Kind:             coordinator.UpdateCallback,
		ActorID:          42,
		ConversationID:   42,
		ConversationKind: "private",
		Text:             "/status",
		CallbackQueryID:  "callback-1",
		SourceMessageID:  9,
	})
	if err != nil {
		t.Fatalf("Handle(callback) error = %v", err)
	}
	want := coordinator.Decision{
		Kind:        coordinator.DecisionBlock,
		BlockReason: "authorized update is not supported",
	}
	if decision != want {
		t.Fatalf("Handle(callback) = %#v, want %#v", decision, want)
	}
}

func TestStatusHandlerSilentlySkipsTransportUnsupportedUpdate(t *testing.T) {
	t.Parallel()

	handler := mustStatusHandler(t, 42, 42)
	decision, err := handler.Handle(context.Background(), coordinator.Update{ID: 101})
	if err != nil {
		t.Fatalf("Handle(unsupported) error = %v", err)
	}
	want := coordinator.Decision{Kind: coordinator.DecisionSkip}
	if decision != want {
		t.Fatalf("Handle(unsupported) = %#v, want %#v", decision, want)
	}
}

func TestNewStatusHandlerRejectsInvalidAuthorizationBoundary(t *testing.T) {
	t.Parallel()

	for _, ids := range [][2]int64{{0, 42}, {-1, 42}, {42, 0}, {42, -1}} {
		if _, err := telegramapp.NewStatusHandler(ids[0], ids[1]); err == nil {
			t.Fatalf("NewStatusHandler(%d, %d) error = nil", ids[0], ids[1])
		}
	}
}

func mustStatusHandler(t *testing.T, ownerUserID, ownerPrivateChatID int64) *telegramapp.StatusHandler {
	t.Helper()
	handler, err := telegramapp.NewStatusHandler(ownerUserID, ownerPrivateChatID)
	if err != nil {
		t.Fatalf("NewStatusHandler() error = %v", err)
	}
	return handler
}
