package telegrampromptcomposition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramstate"
)

// Exercise recognition-in-progress, not a final answer or a manually supplied
// prompt-status page. Only transcription, provider execution and HTTP are fakes.
func TestVoiceRecognitionPendingDividerOnRichWire(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "voice", "local", domain.ProviderCodex, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "voice-provider", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}

	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	notifications := make(chan telegramcontroller.Notification, 8)
	fixture := &voiceSpacingFixture{entered: entered, release: release, finished: finished, notifications: notifications}
	controller, err := telegramcontroller.New(42, 42, "local", fixture, store, fixture, fixture,
		telegramcontroller.Options{UIState: store, Recovered: []domain.Session{ready}, InputPreparer: fixture})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("voice cleanup did not finish")
		}
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := controller.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID(), UpdateID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.HandleSemanticMessage(ctx, coordinator.Update{ID: 2, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "private", MediaKind: "voice", MediaFileID: "synthetic-voice", MediaDownloadAllowed: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recognition did not enter the blocked preparer")
	}
	var notification telegramcontroller.Notification
	select {
	case notification = <-notifications:
	case <-time.After(5 * time.Second):
		t.Fatal("recognition did not publish its pending status")
	}
	if notification.Kind != telegramcontroller.NotificationPromptStatus || notification.Text != "🙋‍♂" {
		t.Fatalf("unexpected recognition notification kind/marker: %s/%q", notification.Kind, notification.Text)
	}

	cards := telegramstate.NewMemoryStore()
	if err := cards.Update(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = ready.ID()
		return state.SetCard(telegramstate.Card{SessionID: ready.ID(), Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 55}, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}})
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 13, 40, 45, 0, time.UTC)
	codec, err := callbacktoken.New(bytes.Repeat([]byte{9}, 32), bytes.NewReader(make([]byte, 4096)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Text      string                         `json:"text"`
		MessageID int64                          `json:"message_id"`
		Rich      *telegram.InputRichMessage     `json:"rich_message"`
		Keyboard  *telegram.InlineKeyboardMarkup `json:"reply_markup"`
	}
	client, err := telegram.NewClient("123:voice-spacing-test", voiceSpacingHTTP(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/editMessageText") {
			t.Fatalf("voice status used unexpected method")
		}
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close(ctx)

	// The pre-A21 header (git HEAD, controller.go) has a soft newline. Keep
	// it as a negative control; current production must preserve the hard break.
	// The actual transport carries Markdown, not HTML: Telegram rendering is
	// deliberately outside this offline test's acceptance boundary.
	for _, test := range []struct {
		name       string
		projection Controller
		want       string
	}{
		{"old-header-reproduction", voiceSpacingOldHeader{controller}, "\n🙋‍♂ Голосовое сообщение"},
		{"current-header-regression", controller, "  \n🙋‍♂ Голосовое сообщение"},
	} {
		t.Run(test.name, func(t *testing.T) {
			deliverer := Deliverer{Controller: test.projection, Cards: cards, Presenter: presenter, Sender: &voiceSpacingSender{bridge}}
			receipt, err := deliverer.Deliver(ctx, notification, "voice:pending:"+test.name)
			if err != nil || receipt.State != "confirmed" || receipt.Suppressed {
				t.Fatalf("pending delivery = %+v, %v", receipt, err)
			}
			if wire.MessageID != 55 || wire.Rich == nil || wire.Text != "" || wire.Keyboard == nil {
				t.Fatal("voice refresh lost Rich payload, carrier or keyboard")
			}
			header, afterDivider, found := strings.Cut(wire.Rich.Markdown, "─────")
			if !found || !strings.Contains(header, " · codex · ") || afterDivider != test.want {
				t.Fatalf("pending voice wire after divider = %q, want %q", afterDivider, test.want)
			}
		})
	}
}

type voiceSpacingOldHeader struct{ *telegramcontroller.Controller }

func (old voiceSpacingOldHeader) ProjectCurrent(ctx context.Context, id domain.SessionID) (telegramcontroller.SemanticActionResult, error) {
	result, err := old.Controller.ProjectCurrent(ctx, id)
	if result.Card != nil {
		result.Card.Header = strings.Replace(result.Card.Header, "─────  \n", "─────\n", 1)
	}
	return result, err
}

type voiceSpacingHTTP func(*http.Request) (*http.Response, error)

func (client voiceSpacingHTTP) Do(request *http.Request) (*http.Response, error) {
	return client(request)
}

type voiceSpacingSender struct{ *telegrambridge.Sender }

func (*voiceSpacingSender) Register(telegramflow.Prepared) error { return nil }

type voiceSpacingFixture struct {
	entered       chan struct{}
	release       <-chan struct{}
	finished      chan<- struct{}
	notifications chan<- telegramcontroller.Notification
}

func (*voiceSpacingFixture) Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	return app.CreateSessionResult{}, errors.New("session creation is outside the voice spacing fixture")
}
func (f *voiceSpacingFixture) Prepare(context.Context, telegramcontroller.IncomingInput) (string, error) {
	close(f.entered)
	<-f.release
	return "synthetic transcription", nil
}
func (*voiceSpacingFixture) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "synthetic completion"}, nil
}
func (f *voiceSpacingFixture) Notify(_ context.Context, notification telegramcontroller.Notification) error {
	if notification.Kind == telegramcontroller.NotificationFinal {
		f.finished <- struct{}{}
	} else {
		f.notifications <- notification
	}
	return nil
}
