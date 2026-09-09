package telegrampromptcomposition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcompletioncomposition"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramstate"
)

type voiceLifecycleNotice struct {
	notification telegramcontroller.Notification
	ack          chan struct{}
}

type voiceLifecycleFixture struct {
	notices                   chan voiceLifecycleNotice
	recognize, complete, stop <-chan struct{}
	submitted                 chan string
}

func (*voiceLifecycleFixture) Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	return app.CreateSessionResult{}, errors.New("unexpected voice session creation")
}
func (f *voiceLifecycleFixture) Prepare(ctx context.Context, _ telegramcontroller.IncomingInput) (string, error) {
	select {
	case <-f.recognize:
		return "RECOGNIZED_VOICE", nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.stop:
		return "", context.Canceled
	}
}
func (f *voiceLifecycleFixture) Submit(ctx context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
	f.submitted <- text
	select {
	case <-f.complete:
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "FINAL_VOICE_ANSWER"}, nil
	case <-ctx.Done():
		return sessionruntime.TurnResult{}, ctx.Err()
	case <-f.stop:
		return sessionruntime.TurnResult{}, context.Canceled
	}
}
func (f *voiceLifecycleFixture) SubmitWithCallbacks(ctx context.Context, id domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if callbacks.OnAccepted == nil {
		return sessionruntime.TurnResult{}, errors.New("missing durable provider acceptance")
	}
	if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	return f.Submit(ctx, id, text)
}
func (f *voiceLifecycleFixture) Notify(ctx context.Context, n telegramcontroller.Notification) error {
	ack := make(chan struct{})
	select {
	case f.notices <- voiceLifecycleNotice{n, ack}:
	case <-f.stop:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
	// The next mutation is forbidden until the current projection reached wire.
	select {
	case <-ack:
		return nil
	case <-f.stop:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestVoiceLifecycleDividerOnEveryRichWirePhase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "voice-lifecycle", "local", domain.ProviderCodex, "/work")
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
	lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "voice-lifecycle", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	wake := make(chan domain.SessionID, 1)
	recognize, complete := make(chan struct{}), make(chan struct{})
	fixture := &voiceLifecycleFixture{notices: make(chan voiceLifecycleNotice, 8), recognize: recognize, complete: complete, stop: ctx.Done(), submitted: make(chan string, 1)}
	c, err := telegramcontroller.New(42, 42, "local", fixture, store, fixture, fixture, telegramcontroller.Options{UIState: store, Recovered: []domain.Session{ready}, InputPreparer: fixture, TurnLifecycle: lifecycle, DurableInput: durablecomposition.InputCustody{Flow: flow, Wake: wake}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		closeCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := c.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID()}); err != nil {
		t.Fatal(err)
	}
	cards := telegramstate.NewMemoryStore()
	if err := cards.Update(ctx, func(s *telegramstate.State) error {
		s.ActiveSession = ready.ID()
		return s.SetCard(telegramstate.Card{SessionID: ready.ID(), Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 55}, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}})
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 13, 40, 45, 0, time.UTC)
	codec, err := callbacktoken.New(bytes.Repeat([]byte{9}, 32), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Rich      *telegram.InputRichMessage     `json:"rich_message"`
		Text      string                         `json:"text"`
		MessageID int64                          `json:"message_id"`
		Keyboard  *telegram.InlineKeyboardMarkup `json:"reply_markup"`
	}
	wireCalls := 0
	client, err := telegram.NewClient("123:voice-lifecycle-test", voiceSpacingHTTP(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/editMessageText") && !strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
			return nil, fmt.Errorf("unexpected voice wire method %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			return nil, err
		}
		wireCalls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close(context.Background())
	prepared := &voiceSpacingSender{sender}
	prompts := Deliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: prepared}
	finals := telegramcompletioncomposition.CompletionDeliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: prepared, ConversationID: 42}
	messageDone := make(chan error, 1)
	processDone := make(chan error, 1)
	go func() {
		select {
		case id := <-wake:
			_, err := flow.ProcessNextInput(ctx, string(id), durablecomposition.NewControllerInputProcessor(c))
			processDone <- err
		case <-ctx.Done():
			processDone <- ctx.Err()
		}
	}()
	go func() {
		_, err := c.HandleSemanticMessage(ctx, coordinator.Update{ID: 600, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42, ConversationKind: "private", MediaKind: "voice", MediaFileID: "synthetic-voice", MediaDownloadAllowed: true})
		messageDone <- err
	}()
	phase := 0
	for phase < 4 {
		var event voiceLifecycleNotice
		select {
		case event = <-fixture.notices:
		case <-time.After(5 * time.Second):
			t.Fatalf("voice lifecycle stopped before phase %d", phase)
		}
		n := event.notification
		before := wireCalls
		operation := fmt.Sprintf("voice-lifecycle:%d", wireCalls)
		if n.Kind == telegramcontroller.NotificationFinal {
			operation = n.OperationID
			if phase != 3 {
				t.Fatalf("final skipped voice phases: %d", phase)
			}
			receipt, err := finals.Deliver(ctx, n, operation)
			if err != nil || receipt.State != "confirmed" || receipt.Suppressed {
				t.Fatalf("final delivery=%+v err=%v", receipt, err)
			}
		} else {
			if n.Kind != telegramcontroller.NotificationPromptStatus {
				t.Fatalf("unexpected lifecycle notice=%+v", n)
			}
			receipt, err := prompts.Deliver(ctx, n, operation)
			if err != nil || receipt.State != "confirmed" || receipt.Suppressed {
				t.Fatalf("prompt delivery=%+v err=%v", receipt, err)
			}
		}
		if wireCalls != before+1 || wire.Rich == nil || wire.Text != "" || wire.Keyboard == nil {
			t.Fatalf("phase %d did not deliver a single Rich keyboard payload", phase)
		}
		header, body, found := strings.Cut(wire.Rich.Markdown, "─────")
		if !found || !strings.Contains(header, " · codex · ") || !strings.HasPrefix(body, "  \n") || strings.HasPrefix(strings.TrimPrefix(body, "  \n"), "\n") {
			t.Fatalf("phase %d divider does not contain exactly one hard newline: %q", phase, body)
		}
		body = strings.TrimPrefix(body, "  \n")
		switch phase {
		case 0:
			if body != "🙋‍♂ Голосовое сообщение" {
				t.Fatalf("pending voice body=%q", body)
			}
			phase++
			close(event.ack)
			close(recognize)
			continue
		case 1:
			if body != "🙋‍♂ RECOGNIZED_VOICE" {
				t.Fatalf("recognized voice body=%q", body)
			}
			phase++
		case 2:
			if body == "🙋‍♂ RECOGNIZED_VOICE" {
				close(event.ack)
				continue
			}
			if body != "👨‍💻 RECOGNIZED_VOICE" {
				t.Fatalf("working voice body=%q", body)
			}
			stored, err := store.Load(ctx, ready.ID())
			if err != nil || stored.Status() != domain.SessionRunning {
				t.Fatalf("working lifecycle=%s err=%v", stored.Status(), err)
			}
			phase++
			close(event.ack)
			select {
			case text := <-fixture.submitted:
				if text != "RECOGNIZED_VOICE" {
					t.Fatalf("provider got %q", text)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("provider never received recognized voice")
			}
			close(complete)
			continue
		case 3:
			if n.Kind != telegramcontroller.NotificationFinal || body != "FINAL_VOICE_ANSWER" {
				t.Fatalf("final voice body=%q notice=%s", body, n.Kind)
			}
			card, _, err := c.ProjectCompletion(ctx, ready.ID())
			if err != nil || len(card.Pages) != 2 || card.Pages[1].Content != "FINAL_VOICE_ANSWER" || strings.Contains(card.Pages[0].Content, "FINAL_VOICE_ANSWER") {
				t.Fatalf("final not isolated from recognized prompt: %+v err=%v", card.Pages, err)
			}
			phase++
		}
		close(event.ack)
	}
	select {
	case err := <-messageDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("voice handler did not finish")
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("durable input did not acknowledge provider custody")
	}
}
