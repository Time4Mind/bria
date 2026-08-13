package telegramapp_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type staticLeader struct{ leader atomic.Bool }

func (l *staticLeader) IsLeader() bool { return l.leader.Load() }

type flowAPI struct {
	updates []telegrambot.Update
	served  atomic.Bool
}

func (a *flowAPI) GetUpdates(ctx context.Context, _ telegrambot.GetUpdatesRequest) ([]telegrambot.Update, error) {
	if !a.served.Swap(true) {
		return a.updates, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*flowAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*flowAPI) SendTyping(context.Context, int64) error                   { return nil }
func (*flowAPI) SendDocument(context.Context, telegrambot.DocumentRequest) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (*flowAPI) SendMessage(context.Context, telegrambot.MessageRequest) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (*flowAPI) EditMessage(context.Context, telegrambot.EditMessageRequest) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}

func TestPollerHandlerAndReplicatedCursorFlow(t *testing.T) {
	fixture := newFixture(t)
	token, err := fixture.codec.Node(7, telegramui.ActionSelectNode, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	callbackData, err := (telegramui.Callback{Action: telegramui.ActionSelectNode, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	api := &flowAPI{updates: []telegrambot.Update{
		{UpdateID: 10, Message: &telegrambot.UpdateMessage{
			MessageID: 1, FromID: 7, ChatID: 7, ChatType: "private", Text: "/start",
		}},
		{UpdateID: 11, CallbackQuery: &telegrambot.CallbackQuery{
			ID: "callback", FromID: 7, Data: callbackData,
			Message: &telegrambot.UpdateMessage{
				MessageID: 1, FromID: 7, ChatID: 7, ChatType: "private",
			},
		}},
	}}
	leader := &staticLeader{}
	leader.leader.Store(true)
	cursor, err := application.NewReplicatedTelegramCursor(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	poller, err := telegrambot.NewPoller(telegrambot.PollerConfig{
		API: api, Leadership: leader, Cursor: cursor, Handler: fixture.handler,
		LongPollTimeout: time.Second, LeadershipCheckInterval: 5 * time.Millisecond,
		RetryDelay: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for fixture.machine.State().TelegramNextUpdateID != 12 {
		select {
		case <-deadline.C:
			t.Fatal("replicated Telegram cursor did not reach 12")
		case <-time.After(time.Millisecond):
		}
	}
	if got := fixture.machine.State().Navigation.ActiveNodeByUser[7]; got != "allowed" {
		t.Fatalf("selected node=%q", got)
	}
	if len(fixture.messenger.sent) != 1 || len(fixture.messenger.edited) != 1 {
		t.Fatalf("sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop")
	}
}

func TestSingleNodeHostFirstMenuFlowOpensActiveCardDirectly(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()
	if err := fixture.handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 20, Kind: telegrambot.IncomingMessage,
		ChatID: 7, UserID: 7, Text: "/start",
	}); err != nil {
		t.Fatal(err)
	}
	openSessions, err := (telegramui.Callback{Action: telegramui.ActionSessions}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 21, Kind: telegrambot.IncomingCallback,
		ChatID: 7, UserID: 7, CallbackID: "sessions", CallbackData: openSessions,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 ||
		fixture.messenger.edited[0].Name != telegramui.ScreenSessionCard {
		t.Fatalf("active card=%#v", fixture.messenger.edited)
	}
}

func callbackForAction(
	t *testing.T,
	screen telegramui.Screen,
	action telegramui.Action,
) string {
	t.Helper()
	for _, row := range screen.Grid {
		for _, button := range row {
			if button.Callback.Action == action {
				encoded, err := button.Callback.Encode()
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			}
		}
	}
	t.Fatalf("screen %s has no %s callback", screen.Name, action)
	return ""
}
