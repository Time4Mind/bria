package telegramapp

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type blockingEditMessenger struct {
	mu           sync.Mutex
	edits        []string
	started      chan string
	releaseFirst chan struct{}
}

func (m *blockingEditMessenger) AnswerCallbackQuery(context.Context, string, string) error {
	return nil
}

func (m *blockingEditMessenger) SendTyping(context.Context, int64) error { return nil }

func (m *blockingEditMessenger) SendDocument(
	context.Context,
	telegrambot.DocumentRequest,
) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}

func (m *blockingEditMessenger) SendScreen(
	context.Context,
	int64,
	telegramui.Screen,
) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}

func (m *blockingEditMessenger) EditScreen(
	_ context.Context,
	origin telegrambot.Message,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	m.mu.Lock()
	index := len(m.edits)
	m.edits = append(m.edits, screen.Text)
	m.mu.Unlock()
	m.started <- screen.Text
	if index == 0 {
		<-m.releaseFirst
	}
	return origin, nil
}

func (m *blockingEditMessenger) DeleteMessage(context.Context, telegrambot.Message) error {
	return nil
}

func (m *blockingEditMessenger) ClearKeyboard(context.Context, telegrambot.Message) error {
	return nil
}

func (m *blockingEditMessenger) editsSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.edits...)
}

func TestExplicitSessionEditWinsOverPaneEditAlreadyInFlight(t *testing.T) {
	messenger := &blockingEditMessenger{
		started: make(chan string, 2), releaseFirst: make(chan struct{}),
	}
	handler := &Handler{
		messenger: messenger, paneRefreshState: newPaneRefreshState(),
	}
	actor := application.Principal{UserID: domain.UserID(7)}
	handler.paneGeneration[actor.UserID] = 4
	handler.paneWorkers[actor.UserID] = 4
	origin := telegrambot.Message{ChatID: 7, MessageID: 11}

	paneDone := make(chan struct{})
	go func() {
		release, err := handler.responseCards.acquire(context.Background(), actor.UserID)
		if err != nil {
			close(paneDone)
			return
		}
		_, _ = handler.messenger.EditScreen(
			context.Background(), origin, telegramui.Screen{Text: "pane"},
		)
		release()
		close(paneDone)
	}()
	if got := <-messenger.started; got != "pane" {
		t.Fatalf("first edit = %q, want pane", got)
	}

	explicitDone := make(chan error, 1)
	go func() {
		_, err := handler.editExplicitSessionScreen(
			context.Background(), actor, origin, telegramui.Screen{Text: "control"},
		)
		explicitDone <- err
	}()
	select {
	case got := <-messenger.started:
		t.Fatalf("explicit edit overtook the in-flight pane edit: %q", got)
	case <-time.After(25 * time.Millisecond):
	}

	close(messenger.releaseFirst)
	<-paneDone
	if err := <-explicitDone; err != nil {
		t.Fatalf("explicit edit: %v", err)
	}
	if got := <-messenger.started; got != "control" {
		t.Fatalf("last edit = %q, want control", got)
	}
	if got, want := messenger.editsSnapshot(), []string{"pane", "control"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("edit order = %v, want %v", got, want)
	}
	if handler.currentPaneGeneration(actor.UserID, 4) {
		t.Fatal("explicit edit did not invalidate the live pane generation")
	}
	handler.paneMu.Lock()
	_, workerRunning := handler.paneWorkers[actor.UserID]
	handler.paneMu.Unlock()
	if workerRunning {
		t.Fatal("explicit edit left the stale pane worker registered")
	}
}
