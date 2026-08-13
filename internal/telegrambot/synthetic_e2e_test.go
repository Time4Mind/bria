package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type syntheticCursor struct {
	mu     sync.Mutex
	offset int64
}

func (c *syntheticCursor) Load(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offset, nil
}
func (c *syntheticCursor) Commit(_ context.Context, offset int64) error {
	c.mu.Lock()
	c.offset = offset
	c.mu.Unlock()
	return nil
}

type syntheticLeader struct{}

func (syntheticLeader) IsLeader() bool { return true }

func TestSyntheticBotAPIE2EGetUpdatesToRenderedMenu(t *testing.T) {
	var mu sync.Mutex
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[len("/bot"+testToken+"/"):]
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case "getUpdates":
			writeJSON(t, writer, map[string]any{"ok": true, "result": []any{map[string]any{
				"update_id": 41, "message": map[string]any{
					"message_id": 4, "from": map[string]any{"id": 7, "language_code": "en"},
					"chat": map[string]any{"id": 7, "type": "private"}, "text": "/menu",
				},
			}}})
		case "sendMessage":
			var payload sendMessagePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.ChatID != 7 || len(payload.ReplyMarkup.InlineKeyboard) != 3 ||
				payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData != "sessions" {
				t.Fatalf("rendered menu payload=%+v", payload)
			}
			writeJSON(t, writer, map[string]any{
				"ok": true, "result": map[string]any{"message_id": 5, "chat": map[string]any{"id": 7}},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	cursor := &syntheticCursor{}
	handled := make(chan struct{}, 1)
	poller, err := NewPoller(PollerConfig{
		API: client, Leadership: syntheticLeader{}, Cursor: cursor,
		Handler: UpdateHandlerFunc(func(ctx context.Context, update IncomingUpdate) error {
			if update.UserID != 7 || update.Text != "/menu" {
				t.Fatalf("incoming=%+v", update)
			}
			_, sendErr := client.SendScreen(ctx, update.ChatID,
				telegramui.RenderMainMenu(i18n.For("en"), ""))
			handled <- struct{}{}
			return sendErr
		}),
		LongPollTimeout: time.Second, LeadershipCheckInterval: time.Millisecond,
		RetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	select {
	case <-handled:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("synthetic update was not handled")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("poller exit=%v", err)
	}
	cursor.mu.Lock()
	offset := cursor.offset
	cursor.mu.Unlock()
	if offset != 42 {
		t.Fatalf("cursor=%d", offset)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) < 2 || methods[0] != "getUpdates" || methods[1] != "sendMessage" {
		t.Fatalf("methods=%v", methods)
	}
}
