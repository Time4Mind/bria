package telegramapp_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type failingBackendSetup struct{ err error }

func (s failingBackendSetup) Start(
	context.Context, backendsetup.Request,
) (backendsetup.Status, error) {
	return backendsetup.Status{}, s.err
}

func (s failingBackendSetup) Status(
	context.Context, backendsetup.Request,
) (backendsetup.Status, error) {
	return backendsetup.Status{}, s.err
}

type machinePort struct {
	machine *clusterstate.Machine
	events  *[]string
}

func (p machinePort) State() *domain.State { return p.machine.State() }
func (p machinePort) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	if p.events != nil {
		*p.events = append(*p.events, "apply")
	}
	return p.machine.Apply(command), nil
}

type messengerStub struct {
	mu                                                sync.Mutex
	answers                                           []string
	sent                                              []telegramui.Screen
	edited                                            []telegramui.Screen
	editedMessages                                    []telegrambot.Message
	deleted                                           []telegrambot.Message
	cleared                                           []telegrambot.Message
	editErr                                           error
	sendErr                                           error
	events                                            *[]string
	editNotify, sendNotify, deleteNotify, clearNotify chan struct{}
}

func (m *messengerStub) AnswerCallbackQuery(_ context.Context, id, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events != nil {
		*m.events = append(*m.events, "answer")
	}
	m.answers = append(m.answers, id+":"+text)
	return nil
}

func (m *messengerStub) SendTyping(context.Context, int64) error { return nil }
func (m *messengerStub) SendDocument(context.Context, telegrambot.DocumentRequest) (telegrambot.Message, error) {
	return telegrambot.Message{ChatID: 7, MessageID: 1}, nil
}
func (m *messengerStub) SendScreen(_ context.Context, _ int64, screen telegramui.Screen) (telegrambot.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, screen)
	if m.sendNotify != nil {
		select {
		case m.sendNotify <- struct{}{}:
		default:
		}
	}
	if m.sendErr != nil {
		return telegrambot.Message{}, m.sendErr
	}
	return stubMessageForScreen(len(m.sent), screen), nil
}
func (m *messengerStub) EditScreen(_ context.Context, message telegrambot.Message, screen telegramui.Screen) (telegrambot.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events != nil {
		*m.events = append(*m.events, "edit")
	}
	m.edited = append(m.edited, screen)
	m.editedMessages = append(m.editedMessages, message)
	if m.editNotify != nil {
		select {
		case m.editNotify <- struct{}{}:
		default:
		}
	}
	if m.editErr != nil {
		return telegrambot.Message{}, m.editErr
	}
	return message, nil
}
func (m *messengerStub) DeleteMessage(_ context.Context, message telegrambot.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, message)
	notifyTest(m.deleteNotify)
	return nil
}
func (m *messengerStub) ClearKeyboard(_ context.Context, message telegrambot.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleared = append(m.cleared, message)
	notifyTest(m.clearNotify)
	return nil
}

func (m *messengerStub) screensSnapshot() (sent, edited []telegramui.Screen, deleted []telegrambot.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sent), slices.Clone(m.edited), slices.Clone(m.deleted)
}

func (m *messengerStub) discardEventTrace() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

type fixture struct {
	handler   *telegramapp.Handler
	service   *application.Service
	projector *application.TelegramProjector
	machine   *clusterstate.Machine
	codec     *callbacktoken.Codec
	messenger *messengerStub
	events    *[]string
}

func TestMessageOpensActorMenuAndUnknownActorIsDropped(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 1, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7, Text: "/start",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 || fixture.messenger.sent[0].Name != telegramui.ScreenMenu {
		t.Fatalf("sent screens=%#v", fixture.messenger.sent)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 2, Kind: telegrambot.IncomingMessage, ChatID: 99, UserID: 99, Text: "/start",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 {
		t.Fatal("unknown actor received a screen")
	}
}

func TestNodeCallbackResolvesOpaqueTokenAndReplicatesSelection(t *testing.T) {
	fixture := newFixture(t)
	token, err := fixture.codec.Node(7, telegramui.ActionSelectNode, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionSelectNode, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 3, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "callback", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Navigation.ActiveNodeByUser[7]; got != "allowed" {
		t.Fatalf("active node=%q", got)
	}
	if len(fixture.messenger.answers) != 1 || len(fixture.messenger.edited) != 1 ||
		fixture.messenger.edited[0].Name != telegramui.ScreenSessionCard {
		t.Fatalf("answers=%#v edited=%#v", fixture.messenger.answers, fixture.messenger.edited)
	}
}

func TestStatusFromLegacyMenuUsesNewRichCarrier(t *testing.T) {
	fixture := newFixture(t)
	data, err := (telegramui.Callback{Action: telegramui.ActionStatus}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fixture.handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 3, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "status", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 || !fixture.messenger.sent[0].RichMarkdown ||
		fixture.messenger.sent[0].Name != telegramui.ScreenStatus {
		t.Fatalf("sent=%#v", fixture.messenger.sent)
	}
	if len(fixture.messenger.edited) != 0 {
		t.Fatalf("legacy menu was edited with rich table: %#v", fixture.messenger.edited)
	}
	if len(fixture.messenger.deleted) != 1 || fixture.messenger.deleted[0].MessageID != 10 {
		t.Fatalf("obsolete menu carrier=%#v", fixture.messenger.deleted)
	}
}
