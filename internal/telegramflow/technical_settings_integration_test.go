package telegramflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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
	for clicks, want := range []int{20, 3, 5, 10} {
		t.Run(fmt.Sprint(want), func(t *testing.T) {
			exerciseTechnicalSettingsWire(t, clicks+1, want, 0, 0, "", false)
		})
	}
}

func exerciseTechnicalSettingsWire(t *testing.T, outputClicks, want, commandClicks, wantCommands int, legacyDocument string, outputFirst bool) {
	t.Helper()
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
	arguments := ""
	if wantCommands > 0 {
		var commands []string
		for i := 1; i <= 21; i++ {
			commands = append(commands, fmt.Sprintf("CMD%02d", i))
		}
		arguments = strings.Join(commands, "\n")
	}
	tool := cardtranscript.EncodeTool(cardtranscript.Tool{ID: "tool", Name: "read", Arguments: arguments, Output: strings.Join(lines, "\n"), Status: "completed"})
	if err := store.SetCardPrompt(ctx, flowSessionID, "prompt-1", "PROMPT"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardTypedHistory(ctx, flowSessionID, tool, "tool"); err != nil {
		t.Fatal(err)
	}
	historyBefore, err := store.LoadCardTranscript(ctx, flowSessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "settings.json")
	if legacyDocument != "" {
		if err := os.WriteFile(path, []byte(legacyDocument), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
		decoded, err := presenter.DecodeCallback(token)
		if err != nil {
			t.Fatalf("unsigned wire token: %v", err)
		}
		if label == "Строки команды" && string(decoded.Action) != "settings_technical_command_lines" {
			t.Fatalf("command button signed wrong action: %s", decoded.Action)
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
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("duplicate callback changed preference: %v", err)
		}
	}
	click("Настройки")
	click("🧾 Содержимое карточки")
	cycleCommands := func() {
		t.Helper()
		outputLimit := 10
		if outputFirst {
			outputLimit = want
		}
		for i := 0; i < commandClicks; i++ {
			click("Строки команды")
			current, err := prefs.Load(ctx)
			if err != nil || current.TechnicalOutputLines != outputLimit {
				t.Fatalf("command callback changed output: %+v want=%d err=%v", current, outputLimit, err)
			}
		}
	}
	cycleOutput := func() {
		t.Helper()
		commandLimit := wantCommands
		if commandLimit == 0 || (outputFirst && commandClicks > 0) {
			commandLimit = 10
		}
		for i := 0; i < outputClicks; i++ {
			click("Строки технического вывода")
			current, err := prefs.Load(ctx)
			if err != nil || current.TechnicalCommandLines != commandLimit {
				t.Fatalf("output callback changed command limit: %+v want=%d err=%v", current, commandLimit, err)
			}
		}
	}
	if outputFirst {
		cycleOutput()
		cycleCommands()
	} else {
		cycleCommands()
		cycleOutput()
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
	if wantCommands > 0 {
		if got.TechnicalCommandLines != wantCommands {
			t.Fatalf("reopened command limit=%d want=%d", got.TechnicalCommandLines, wantCommands)
		}
		document, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var persisted struct {
			Command int `json:"technical_command_lines"`
			Output  int `json:"technical_output_lines"`
		}
		if err := json.Unmarshal(document, &persisted); err != nil || persisted.Command != wantCommands || persisted.Output != want {
			t.Fatalf("physical independent choices=%+v want=%d+%d err=%v", persisted, wantCommands, want, err)
		}
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	store, err = storage.OpenSessionStore(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	cards.State = store
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
	if wantCommands > 0 {
		assertIndependentTechnicalWire(t, result.Rich.Markdown, wantCommands, want)
	}
	historyAfter, err := store.LoadCardTranscript(ctx, flowSessionID, true)
	if err != nil || !reflect.DeepEqual(historyAfter, historyBefore) {
		t.Fatalf("settings/reopen changed stored transcript: before=%+v after=%+v err=%v", historyBefore, historyAfter, err)
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
