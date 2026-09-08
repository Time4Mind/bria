package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcompletioncomposition"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegrampromptcomposition"
	"bria/internal/telegramruntimecomposition"
)

// Use actual semantic projections, composition, signed flow and JSON transport.
// The only HTTP implementation is an in-memory receiver; no live API is called.
func TestCardSpacingAcceptanceControllerToWire(t *testing.T) {
	for _, body := range []struct{ name, markdown, plain, tableRich string }{
		{"ordinary", "**FINAL ANSWER**", "FINAL ANSWER", ""},
		{"code", "```go\nfinalAnswer()\n```", "finalAnswer()\n", ""},
		{"list", "- **FINAL ITEM**\n- Second", "- FINAL ITEM\n- Second", ""},
		{"two-tables", "| A | B |\n|---|---|\n| one | two |\n\n| C | D |\n|---|---|\n| three | four |", "",
			"\n| <sub>A</sub> | <sub>B</sub> |\n|---|---|\n| <sub>one</sub> | <sub>two</sub> |\n\n| <sub>C</sub> | <sub>D</sub> |\n|---|---|\n| <sub>three</sub> | <sub>four</sub> |"},
	} {
		for _, mode := range []string{"plain", "rich"} {
			t.Run(body.name+"/"+mode, func(t *testing.T) {
				wantMode, wantMarkdown := "rich", body.markdown
				completionMode := "rich"
				if body.tableRich != "" {
					wantMode, completionMode, wantMarkdown = "rich", "rich", body.tableRich
				}
				ctx := context.Background()
				store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				session, err := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "spacing", "local", domain.ProviderCodex, "/work")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
					t.Fatal(err)
				}
				ready, err := session.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "spacing-provider", Generation: 1})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.CompareAndSwap(ctx, session, ready); err != nil {
					t.Fatal(err)
				}
				session = ready
				if err := store.SetCardPrompt(ctx, session.ID(), "prompt-1", "PRECEDING PROMPT"); err != nil {
					t.Fatal(err)
				}
				if err := store.InsertCardTypedHistoryAfterPrompt(ctx, session.ID(), "prompt-1", "PRECEDING COMMENTARY", "commentary"); err != nil {
					t.Fatal(err)
				}
				if err := store.InsertCardTypedHistoryAfterPrompt(ctx, session.ID(), "prompt-1", body.markdown, "final"); err != nil {
					t.Fatal(err)
				}
				controller, err := telegramcontroller.New(42, 42, "local", staticCreator{session: session}, store, flowSubmitter{}, discardNotifier{}, telegramcontroller.Options{UIState: store})
				if err != nil {
					t.Fatal(err)
				}
				defer controller.Close(ctx)
				if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: session.ID(), UpdateID: 1}); err != nil {
					t.Fatal(err)
				}
				codec, err := callbacktoken.New([]byte(strings.Repeat("k", 32)), rand.Reader, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				presenter, err := telegrambridge.NewPresenter(codec, time.Now, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				httpClient := &cardSpacingAcceptanceHTTP{t: t}
				client, err := telegram.NewClient("123:spacing-acceptance", httpClient, telegram.Options{})
				if err != nil {
					t.Fatal(err)
				}
				bridge, err := telegrambridge.NewSender(client)
				if err != nil {
					t.Fatal(err)
				}
				defer bridge.Close(ctx)
				if mode == "rich" {
					if err := bridge.BindScreenSource(cardSpacingNoScreen{}); err != nil {
						t.Fatal(err)
					}
				}
				adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: controller}
				uiStore := telegramruntimecomposition.SessionTelegramUIStore{State: store}
				handler, outbound, err := telegramflow.New(telegramflow.Config{
					OwnerUserID: 42, OwnerPrivateChatID: 42, Presenter: presenter,
					CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(time.Now), UIState: uiStore,
					MessageUI: adapter, Callbacks: adapter, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: bridge,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Run("runtime-send", func(t *testing.T) {
					decision, err := handler.Handle(ctx, coordinator.Update{ID: 2, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "private", Text: "/status"})
					if err != nil || decision.Keyboard == nil {
						t.Fatalf("runtime projection = %+v, %v", decision, err)
					}
					if _, err := outbound.SendStatusWithKeyboard(ctx, "status:2", decision.Status, decision.Keyboard); err != nil {
						t.Fatal(err)
					}
					cardSpacingAssertWire(t, httpClient, wantMode, "send", wantMarkdown, body.plain)
				})
				t.Run("prompt-edit", func(t *testing.T) {
					deliverer := telegrampromptcomposition.Deliverer{Controller: controller, Cards: uiStore, Presenter: presenter, Sender: outbound}
					receipt, err := deliverer.Deliver(ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationPromptStatus, SessionID: session.ID()}, "spacing:prompt")
					if err != nil || receipt.State != "confirmed" {
						t.Fatalf("prompt receipt = %+v, %v", receipt, err)
					}
					cardSpacingAssertWire(t, httpClient, wantMode, "edit", wantMarkdown, body.plain)
				})
				t.Run("completion-send", func(t *testing.T) {
					deliverer := telegramcompletioncomposition.CompletionDeliverer{Controller: controller, Cards: uiStore, Presenter: presenter, Sender: outbound, ConversationID: 42}
					receipt, err := deliverer.Deliver(ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationFinal, SessionID: session.ID()}, "spacing:completion")
					if err != nil || receipt.State != "confirmed" {
						t.Fatalf("completion receipt = %+v, %v", receipt, err)
					}
					// Session cards stay Rich even on pages without tables, so
					// navigation never downgrades the carrier to plain text.
					cardSpacingAssertWire(t, httpClient, completionMode, "send", wantMarkdown, body.plain)
				})
			})
		}
	}
}

type cardSpacingNoScreen struct{}

func (cardSpacingNoScreen) ScreenPNG(context.Context, string) ([]byte, error) { return nil, nil }

type cardSpacingAcceptanceHTTP struct {
	t         *testing.T
	method    string
	text      string
	rich      *telegram.InputRichMessage
	keyboard  *telegram.InlineKeyboardMarkup
	messageID int64
}

func (receiver *cardSpacingAcceptanceHTTP) Do(request *http.Request) (*http.Response, error) {
	var body struct {
		Text      string                         `json:"text"`
		Rich      *telegram.InputRichMessage     `json:"rich_message"`
		Keyboard  *telegram.InlineKeyboardMarkup `json:"reply_markup"`
		MessageID int64                          `json:"message_id"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		receiver.t.Fatal(err)
	}
	receiver.method, receiver.text, receiver.rich, receiver.keyboard, receiver.messageID = request.URL.Path, body.Text, body.Rich, body.Keyboard, body.MessageID
	return statusAcceptanceResponse(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
}

func cardSpacingAssertWire(t *testing.T, wire *cardSpacingAcceptanceHTTP, mode, action, markdown, plain string) {
	t.Helper()
	wantMethod := "/sendMessage"
	text, wantBody := wire.text, "\n"+plain
	if mode == "rich" {
		wantMethod = "/sendRichMessage"
		if wire.rich == nil {
			t.Fatal("missing rich wire message")
		}
		text, wantBody = wire.rich.Markdown, "  \n"+markdown
	} else if wire.rich != nil {
		t.Fatal("unexpected rich wire message")
	}
	if action == "edit" {
		wantMethod = "/editMessageText"
		if wire.messageID != 55 {
			t.Errorf("edit carrier = %d, want 55", wire.messageID)
		}
	}
	if !strings.HasSuffix(wire.method, wantMethod) {
		t.Errorf("wire method = %q, want %q", wire.method, wantMethod)
	}
	header, body, found := strings.Cut(text, "─────")
	if !found || !strings.Contains(header, " · codex · ") || body != wantBody {
		t.Errorf("wire after divider = %q, want %q; full text = %q", body, wantBody, text)
	}
	// The final must own its page on the actual wire, not share it with
	// the preceding prompt/commentary. The signed page label proves 2/2.
	if strings.Contains(text, "PRECEDING") {
		t.Error("final wire page contains preceding history")
	}
	if wire.keyboard == nil {
		t.Fatal("missing signed card keyboard")
	}
	for _, row := range wire.keyboard.InlineKeyboard {
		for _, button := range row {
			if button.Text == "2/2" {
				return
			}
		}
	}
	t.Errorf("wire keyboard has no dedicated final page 2/2: %+v", wire.keyboard)
}
