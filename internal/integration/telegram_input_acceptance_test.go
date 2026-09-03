package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

// TestTelegramReplyAndMediaSurviveTransportNormalization proves the input
// metadata that routing and media processing consume at their shared public
// boundary. It also proves a reply can target its origin session without
// changing the user's active session.
func TestTelegramReplyAndMediaSurviveTransportNormalization(t *testing.T) {
	const owner = int64(42)
	body := `{"ok":true,"result":[
  {"update_id":101,"message":{"message_id":201,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"reply to background","reply_to_message":{"message_id":190}}},
  {"update_id":102,"message":{"message_id":202,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"voice caption","voice":{"file_id":"voice-id","file_unique_id":"voice-unique","duration":7,"mime_type":"audio/ogg","file_size":4096}}},
  {"update_id":103,"message":{"message_id":203,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"photo caption","photo":[{"file_id":"wide","file_unique_id":"wide-u","width":400,"height":100,"file_size":200},{"file_id":"largest","file_unique_id":"largest-u","width":300,"height":300,"file_size":9000}]}},
  {"update_id":104,"message":{"message_id":204,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"video caption only","video":{"file_id":"video-id","file_unique_id":"video-unique","width":1920,"height":1080,"duration":12,"mime_type":"video/mp4","file_size":2000000}}},
  {"update_id":105,"message":{"message_id":205,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"document caption only","document":{"file_id":"document-id","file_unique_id":"document-unique","file_name":"report.txt","mime_type":"text/plain","file_size":100}}}
]}`
	transport := &singleTelegramResponse{body: body}
	client, err := telegram.NewClient("123:acceptance-token", transport, telegram.Options{})
	if err != nil {
		t.Fatalf("create Telegram client: %v", err)
	}
	source, err := telegrambridge.NewSource(client)
	if err != nil {
		t.Fatalf("create Telegram source: %v", err)
	}
	updates, err := source.Poll(context.Background(), 101)
	if err != nil {
		t.Fatalf("poll normalized updates: %v", err)
	}
	if len(updates) != 5 {
		t.Fatalf("normalized update count = %d, want 5", len(updates))
	}
	if got := updates[0]; got.Text != "reply to background" || got.SourceMessageID != 201 || got.ReplyToMessageID != 190 {
		t.Fatalf("normalized reply = %#v", got)
	}
	if got := updates[1]; got.Caption != "voice caption" || got.MediaKind != "voice" ||
		got.MediaFileID != "voice-id" || got.MediaFileUniqueID != "voice-unique" ||
		got.MediaFileSize != 4096 || got.MediaMIMEType != "audio/ogg" ||
		got.MediaDurationSeconds != 7 || !got.MediaDownloadAllowed {
		t.Fatalf("normalized voice = %#v", got)
	}
	if got := updates[2]; got.Caption != "photo caption" || got.MediaKind != "photo" ||
		got.MediaFileID != "largest" || got.MediaFileUniqueID != "largest-u" ||
		got.MediaFileSize != 9000 || got.MediaWidth != 300 || got.MediaHeight != 300 ||
		!got.MediaDownloadAllowed {
		t.Fatalf("normalized largest photo = %#v", got)
	}
	if got := updates[3]; got.Caption != "video caption only" || got.MediaKind != "video" ||
		got.MediaFileID != "video-id" || got.MediaDownloadAllowed || got.MediaWidth != 1920 ||
		got.MediaHeight != 1080 || got.MediaDurationSeconds != 12 {
		t.Fatalf("normalized video policy = %#v", got)
	}
	if got := updates[4]; got.Caption != "document caption only" || got.MediaKind != "" || got.MediaDownloadAllowed {
		t.Fatalf("normalized document caption policy = %#v", got)
	}

	active := mustIntegrationReadySession(t, "11111111-1111-4111-9111-111111111111", "intent-active", domain.ProviderCodex, "codex-active")
	background := mustIntegrationReadySession(t, "22222222-2222-4222-9222-222222222222", "intent-background", domain.ProviderClaude, "claude-background")
	routePath := filepath.Join(t.TempDir(), "telegram-reply-routes.json")
	routes, err := storage.OpenTelegramReplyRouteStore(routePath, owner, owner)
	if err != nil {
		t.Fatalf("open durable reply routes: %v", err)
	}
	notificationClient, err := telegram.NewClient("123:acceptance-token", integrationHTTPFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/sendMessage") {
			return nil, errors.New("unexpected Telegram notification request")
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":190,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"done"}}`)),
		}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatalf("create notification client: %v", err)
	}
	notifier, err := telegramnotify.NewWithOptions(notificationClient, telegramnotify.Options{
		ReceiptRecorder: storageReceiptRecorder{store: routes},
	})
	if err != nil {
		t.Fatalf("create receipt-recording notifier: %v", err)
	}
	if err := notifier.Notify(context.Background(), telegramcontroller.Notification{
		ConversationID: owner, SessionID: background.ID(), Kind: telegramcontroller.NotificationFinal, Text: "background done",
	}); err != nil {
		t.Fatalf("deliver confirmed background final: %v", err)
	}
	routes, err = storage.OpenTelegramReplyRouteStore(routePath, owner, owner)
	if err != nil {
		t.Fatalf("reopen durable reply routes: %v", err)
	}
	submitter := &capturingSubmitter{calls: make(chan submittedTurn, 2)}
	controller, err := telegramcontroller.New(
		owner, owner, "local", staticCreator{session: active},
		&integrationSessions{byID: map[domain.SessionID]domain.Session{active.ID(): active, background.ID(): background}},
		submitter, discardNotifier{}, telegramcontroller.Options{
			Recovered:   []domain.Session{active, background},
			ReplyRoutes: routes,
		},
	)
	if err != nil {
		t.Fatalf("create Telegram controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	if decision, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 100, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: owner,
		ConversationKind: "private", Text: "/use " + string(active.ID()),
	}); err != nil || decision.Kind != coordinator.DecisionStatus {
		t.Fatalf("select active session = (%#v, %v)", decision, err)
	}
	if decision, err := controller.Handle(context.Background(), updates[0]); err != nil || decision.Kind != coordinator.DecisionStatus {
		t.Fatalf("route normalized reply = (%#v, %v)", decision, err)
	}
	assertSubmittedTurn(t, submitter.calls, submittedTurn{sessionID: background.ID(), text: "reply to background"})

	if decision, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 106, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: owner,
		ConversationKind: "private", Text: "ordinary active turn",
	}); err != nil || decision.Kind != coordinator.DecisionStatus {
		t.Fatalf("submit ordinary active turn = (%#v, %v)", decision, err)
	}
	assertSubmittedTurn(t, submitter.calls, submittedTurn{sessionID: active.ID(), text: "ordinary active turn"})
}

// TestProductSurfacesExposeNoClearAction protects the explicit product
// decision across all currently reachable controller keyboards and the hidden
// callback boundary, rather than checking only one exact button spelling.
func TestProductSurfacesExposeNoClearAction(t *testing.T) {
	const owner = int64(42)
	session := mustIntegrationReadySession(t, "33333333-3333-4333-9333-333333333333", "intent", domain.ProviderCodex, "codex-thread")
	submitter := &capturingSubmitter{calls: make(chan submittedTurn, 1)}
	controller, err := telegramcontroller.New(
		owner, owner, "local", staticCreator{session: session},
		&integrationSessions{byID: map[domain.SessionID]domain.Session{session.ID(): session}},
		submitter, discardNotifier{}, telegramcontroller.Options{Recovered: []domain.Session{session}},
	)
	if err != nil {
		t.Fatalf("create Telegram controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	updates := []coordinator.Update{
		{ID: 0, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: owner, ConversationKind: "private", Text: "/use " + string(session.ID())},
		{ID: 1, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: owner, ConversationKind: "private", Text: "/menu"},
		{ID: 2, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: owner, ConversationKind: "private", Text: "/status"},
		{ID: 3, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: owner, ConversationKind: "private", Text: "/sessions"},
		{ID: 4, Kind: coordinator.UpdateCallback, ActorID: owner, ConversationID: owner, ConversationKind: "private", CallbackQueryID: "q4", SourceMessageID: 77, Text: "mm:new"},
		{ID: 5, Kind: coordinator.UpdateCallback, ActorID: owner, ConversationID: owner, ConversationKind: "private", CallbackQueryID: "q5", SourceMessageID: 77, Text: "mm:arch"},
		{ID: 6, Kind: coordinator.UpdateCallback, ActorID: owner, ConversationID: owner, ConversationKind: "private", CallbackQueryID: "q6", SourceMessageID: 77, Text: "mm:set"},
		{ID: 7, Kind: coordinator.UpdateCallback, ActorID: owner, ConversationID: owner, ConversationKind: "private", CallbackQueryID: "q7", SourceMessageID: 77, Text: "ft:more"},
	}
	for _, update := range updates {
		decision, err := controller.Handle(context.Background(), update)
		if err != nil {
			t.Fatalf("surface %q: %v", update.Text, err)
		}
		assertNoClearDecision(t, update.Text, decision)
	}

	decision, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 8, Kind: coordinator.UpdateCallback, ActorID: owner, ConversationID: owner,
		ConversationKind: "private", CallbackQueryID: "q8", SourceMessageID: 77, Text: "ft:clear",
	})
	if err != nil || decision.Kind != coordinator.DecisionSkip {
		t.Fatalf("forbidden hidden clear callback = (%#v, %v), want ignored", decision, err)
	}
	select {
	case call := <-submitter.calls:
		t.Fatalf("forbidden clear callback reached provider: %#v", call)
	case <-time.After(50 * time.Millisecond):
	}
	command, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 9, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: owner,
		ConversationKind: "private", Text: "/clear",
	})
	if err != nil || command.Kind != coordinator.DecisionStatus {
		t.Fatalf("literal /clear prompt = (%#v, %v), want ordinary accepted input", command, err)
	}
	assertSubmittedTurn(t, submitter.calls, submittedTurn{sessionID: session.ID(), text: "/clear"})
}

func assertNoClearDecision(t *testing.T, surface string, decision coordinator.Decision) {
	t.Helper()
	for _, value := range []string{decision.Status.Text, decision.BlockReason} {
		lower := strings.ToLower(value)
		// Status bodies contain caller-controlled workdirs and provider text;
		// only the Russian product action label is meaningful there. Callback
		// data and button labels below are fully Bria-controlled.
		if strings.Contains(lower, "очист") {
			t.Fatalf("surface %q exposes forbidden clear text %q", surface, value)
		}
	}
	if decision.Keyboard == nil {
		return
	}
	for _, row := range *decision.Keyboard {
		for _, button := range row {
			for _, value := range []string{button.Text, button.CallbackData} {
				lower := strings.ToLower(value)
				if strings.Contains(lower, "clear") || strings.Contains(lower, "очист") {
					t.Fatalf("surface %q exposes forbidden clear button %#v", surface, button)
				}
			}
		}
	}
}

type singleTelegramResponse struct {
	mu    sync.Mutex
	body  string
	calls int
}

type integrationHTTPFunc func(*http.Request) (*http.Response, error)

func (function integrationHTTPFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type storageReceiptRecorder struct {
	store *storage.TelegramReplyRouteStore
}

func (recorder storageReceiptRecorder) RecordOutboundReceipt(ctx context.Context, receipt telegramnotify.OutboundReceipt) error {
	return recorder.store.RecordOutboundReceipt(ctx, storage.TelegramOutboundReceipt{
		MessageID: receipt.MessageID, SessionID: receipt.SessionID,
	})
}

func (client *singleTelegramResponse) Do(request *http.Request) (*http.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.calls++
	if client.calls != 1 || !strings.HasSuffix(request.URL.Path, "/getUpdates") {
		return nil, errors.New("unexpected Telegram request")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(client.body)),
	}, nil
}

type staticCreator struct{ session domain.Session }

func (creator staticCreator) Create(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	snapshot := creator.session.Snapshot()
	snapshot.IntentID = intent.IntentID
	session, err := domain.RestoreSession(snapshot)
	return app.CreateSessionResult{Session: session}, err
}

type integrationSessions struct {
	byID map[domain.SessionID]domain.Session
}

func (sessions *integrationSessions) List(context.Context) ([]domain.Session, error) {
	result := make([]domain.Session, 0, len(sessions.byID))
	for _, session := range sessions.byID {
		result = append(result, session)
	}
	return result, nil
}

func (sessions *integrationSessions) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	session, ok := sessions.byID[id]
	if !ok {
		return domain.Session{}, errors.New("session not found")
	}
	return session, nil
}

type submittedTurn struct {
	sessionID domain.SessionID
	text      string
}

type capturingSubmitter struct{ calls chan submittedTurn }

func (submitter *capturingSubmitter) Submit(_ context.Context, sessionID domain.SessionID, text string) (sessionruntime.TurnResult, error) {
	submitter.calls <- submittedTurn{sessionID: sessionID, text: text}
	return sessionruntime.TurnResult{Final: "ok", TerminalStatus: sessionruntime.StatusCompleted}, nil
}

type discardNotifier struct{}

func (discardNotifier) Notify(context.Context, telegramcontroller.Notification) error { return nil }

func mustIntegrationReadySession(
	t *testing.T,
	id domain.SessionID,
	intent domain.IntentID,
	provider domain.Provider,
	providerSessionID string,
) domain.Session {
	t.Helper()
	starting, err := domain.NewStartingSession(id, intent, "local", provider, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: provider, SessionID: providerSessionID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func assertSubmittedTurn(t *testing.T, calls <-chan submittedTurn, want submittedTurn) {
	t.Helper()
	select {
	case got := <-calls:
		if got != want {
			t.Fatalf("submitted turn = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("submitted turn %#v was not observed", want)
	}
}
