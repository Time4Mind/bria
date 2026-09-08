package telegramflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/cardtranscript"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegrampromptcomposition"
	"bria/internal/telegramruntimecomposition"
)

// Only the HTTP transport and unused provider are fake. Menu tokens come from
// real signed wire keyboards, traverse the handler, and persist actual settings.
func TestTechnicalSettingsSignedCallbackReopenRichWire(t *testing.T) {
	for clicks, want := range []int{20, 40, 5, 10} {
		t.Run(fmt.Sprint(want), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, err := storage.OpenSessionStore(filepath.Join(root, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			starting, err := domain.NewStartingSession(flowSessionID, "intent", "local", domain.ProviderCodex, "/synthetic")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
				t.Fatal(err)
			}
			ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CompareAndSwap(ctx, starting, ready); err != nil {
				t.Fatal(err)
			}
			var lines []string
			for i := 1; i <= 41; i++ {
				lines = append(lines, fmt.Sprintf("ROW%02d", i))
			}
			tool := cardtranscript.EncodeTool(cardtranscript.Tool{ID: "tool", Name: "read", Output: strings.Join(lines, "\n"), Status: "completed"})
			if err := store.SetCardPrompt(ctx, flowSessionID, "prompt-1", "PROMPT"); err != nil {
				t.Fatal(err)
			}
			if err := store.AppendCardTypedHistory(ctx, flowSessionID, tool, "tool"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "settings.json")
			prefs, err := settings.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			controller := func(prefs *settings.FileStore) *telegramcontroller.Controller {
				t.Helper()
				c, err := telegramcontroller.New(7, 42, "local", archiveSwitchUnused{}, store, archiveSwitchUnused{}, archiveSwitchNotify(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: store, Settings: settingscomposition.Preferences{Store: prefs}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close(ctx) })
				return c
			}
			c := controller(prefs)
			wire := &technicalSettingsHTTP{}
			client, err := telegram.NewClient("123:synthetic", wire, telegram.Options{})
			if err != nil {
				t.Fatal(err)
			}
			sender, err := telegrambridge.NewSender(client)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sender.Close(ctx) })
			now := time.Unix(1_800_000_000, 0).UTC()
			presenter := newPresenter(t, now)
			cards := telegramruntimecomposition.SessionTelegramUIStore{State: store}
			adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: c}
			handler, outbound, err := telegramflow.New(telegramflow.Config{OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }), UIState: cards, MessageUI: adapter, Callbacks: adapter, Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: sender})
			if err != nil {
				t.Fatal(err)
			}
			update := coordinator.Update{ID: 1, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "/menu"}
			decision, err := handler.Handle(ctx, update)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := outbound.SendStatusWithKeyboard(ctx, "status:1", decision.Status, decision.Keyboard); err != nil {
				t.Fatal(err)
			}
			click := func(label string) {
				t.Helper()
				last := wire.snapshot()
				token := ""
				for _, row := range last.Keyboard.InlineKeyboard {
					for _, b := range row {
						if b.Text == label {
							token = b.CallbackData
						}
					}
				}
				if token == "" {
					t.Fatalf("button %q missing on actual wire: %+v", label, last.Keyboard)
				}
				if _, err := presenter.DecodeCallback(token); err != nil {
					t.Fatalf("unsigned wire token: %v", err)
				}
				update.ID++
				update.Kind, update.Text, update.SourceMessageID, update.CallbackQueryID = coordinator.UpdateCallback, token, 99, fmt.Sprint(update.ID)
				decision, err = handler.Handle(ctx, update)
				if err != nil {
					t.Fatalf("click %q: %v", label, err)
				}
				if _, err := outbound.EditStatusWithKeyboard(ctx, fmt.Sprintf("status:%d", update.ID), decision.Status, decision.Keyboard); err != nil {
					t.Fatal(err)
				}
				// A replay of the same accepted update must not cycle the preference again.
				before, err := prefs.Current(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := handler.Handle(ctx, update); err != nil {
					t.Fatal(err)
				}
				after, err := prefs.Current(ctx)
				if err != nil || after.Revision != before.Revision || after.Settings.TechnicalOutputLines != before.Settings.TechnicalOutputLines {
					t.Fatalf("duplicate callback changed preference: %v", err)
				}
			}
			click("Настройки")
			click("🧾 Содержимое карточки")
			for i := 0; i <= clicks; i++ {
				click("Строки технического вывода")
			}
			if got := wire.snapshot(); got.Rich == nil || !strings.Contains(got.Rich.Markdown, fmt.Sprintf("| <sub>Строки технического вывода</sub> | <sub>%d</sub> |", want)) {
				t.Fatalf("settings wire ignores choice %d: %+v", want, got.Rich)
			}
			reopened, err := settings.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.Load(ctx)
			if err != nil || got.TechnicalOutputLines != want {
				t.Fatalf("reopened lines=%d want=%d err=%v", got.TechnicalOutputLines, want, err)
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			c = controller(reopened)
			if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: flowSessionID}); err != nil {
				t.Fatal(err)
			}
			if err := store.SetCardCarrier(ctx, flowSessionID, 42, 99); err != nil {
				t.Fatal(err)
			}
			delivery := telegrampromptcomposition.Deliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: outbound}
			if _, err := delivery.Deliver(ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationPromptStatus, SessionID: flowSessionID}, "reopened-card"); err != nil {
				t.Fatal(err)
			}
			result := wire.snapshot()
			if result.Rich == nil || !strings.Contains(result.Rich.Markdown, fmt.Sprintf("ROW%02d", want)) || strings.Contains(result.Rich.Markdown, fmt.Sprintf("ROW%02d", want+1)) {
				t.Fatalf("reopened Rich wire ignores %d: %+v", want, result.Rich)
			}
		})
	}
}

type technicalSettingsPayload struct {
	Rich     *telegram.InputRichMessage    `json:"rich_message"`
	Keyboard telegram.InlineKeyboardMarkup `json:"reply_markup"`
}
type technicalSettingsHTTP struct {
	mu   sync.Mutex
	last technicalSettingsPayload
}

func (h *technicalSettingsHTTP) snapshot() technicalSettingsPayload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}
func (h *technicalSettingsHTTP) Do(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
	}
	var payload technicalSettingsPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.last = payload
	h.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":99,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
}
