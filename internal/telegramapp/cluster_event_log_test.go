package telegramapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramoutbound"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type clusterLogMessenger struct {
	nextID int64
	sent   []telegramui.Screen
	edited []telegramui.Screen
}

type clusterLogPort struct{ state *domain.State }

func (p *clusterLogPort) State() *domain.State { return p.state }
func (*clusterLogPort) Apply(
	context.Context, clusterstate.Command,
) (clusterstate.Result, error) {
	return clusterstate.Result{}, domain.ErrInvalidState
}

func (*clusterLogMessenger) AnswerCallbackQuery(context.Context, string, string) error {
	return nil
}
func (*clusterLogMessenger) SendTyping(context.Context, int64) error { return nil }
func (m *clusterLogMessenger) SendDocument(
	context.Context, telegrambot.DocumentRequest,
) (telegrambot.Message, error) {
	m.nextID++
	return telegrambot.Message{ChatID: 7, MessageID: m.nextID}, nil
}
func (m *clusterLogMessenger) SendScreen(
	_ context.Context, chatID int64, screen telegramui.Screen,
) (telegrambot.Message, error) {
	m.nextID++
	m.sent = append(m.sent, screen)
	return telegrambot.Message{ChatID: chatID, MessageID: m.nextID}, nil
}
func (m *clusterLogMessenger) EditScreen(
	_ context.Context, message telegrambot.Message, screen telegramui.Screen,
) (telegrambot.Message, error) {
	m.edited = append(m.edited, screen)
	message.Text = screen.Text
	return message, nil
}
func (*clusterLogMessenger) DeleteMessage(context.Context, telegrambot.Message) error { return nil }
func (*clusterLogMessenger) ClearKeyboard(context.Context, telegrambot.Message) error {
	return nil
}

func TestClusterEventLogEditsOnlyWhileItIsNewest(t *testing.T) {
	base := &clusterLogMessenger{}
	activity := telegramoutbound.New(base)
	handler := &Handler{
		messenger: activity, activity: activity,
		clusterEventLogs: make(map[int64]clusterEventLog),
	}
	target := application.ClusterAlertTarget{
		OwnerID: 7, NodeName: "Альфа", Language: domain.LanguageRussian, Enabled: true,
	}
	first := time.Date(2026, 8, 13, 14, 32, 0, 0, time.UTC)
	if err := handler.appendClusterEvent(context.Background(), target, clusterEvent{
		Kind: clusterEventNodeLost, NodeName: "Альфа", At: first,
	}); err != nil {
		t.Fatal(err)
	}
	if err := handler.appendClusterEvent(context.Background(), target, clusterEvent{
		Kind: clusterEventNodeRestored, NodeName: "Альфа", At: first.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if len(base.sent) != 1 || len(base.edited) != 1 {
		t.Fatalf("sent=%d edited=%d", len(base.sent), len(base.edited))
	}
	if text := base.edited[0].Text; !strings.Contains(text, "14:32 🔴 Альфа") ||
		!strings.Contains(text, "14:33 🟢 Альфа") {
		t.Fatalf("rolling log=%q", text)
	}

	// A user message after the log means the next event must start a new block.
	activity.ObserveIncoming(7, 50)
	base.nextID = 50
	if err := handler.appendClusterEvent(context.Background(), target, clusterEvent{
		Kind: clusterEventLeader, NodeName: "Бета", At: first.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if len(base.sent) != 2 || len(base.edited) != 1 {
		t.Fatalf("after interruption sent=%d edited=%d", len(base.sent), len(base.edited))
	}
	if text := base.sent[1].Text; text != "Кластер\n14:34 👑 Бета" {
		t.Fatalf("new log=%q", text)
	}
}

func TestClusterEventLogKeepsOnlySixNewestEvents(t *testing.T) {
	base := &clusterLogMessenger{}
	activity := telegramoutbound.New(base)
	handler := &Handler{
		messenger: activity, activity: activity,
		clusterEventLogs: make(map[int64]clusterEventLog),
	}
	target := application.ClusterAlertTarget{
		OwnerID: 7, Language: domain.LanguageRussian, Enabled: true,
	}
	for index := 0; index < 8; index++ {
		name := string(rune('A' + index))
		if err := handler.appendClusterEvent(context.Background(), target, clusterEvent{
			Kind: clusterEventLeader, NodeName: name,
			At: time.Date(2026, 8, 13, 10, index, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}
	log := handler.clusterEventLogs[7]
	if len(log.Events) != clusterEventLogLimit || log.Events[0].NodeName != "C" ||
		log.Events[len(log.Events)-1].NodeName != "H" {
		t.Fatalf("events=%#v", log.Events)
	}
}

func TestClusterEventObserverReportsLeaderAndNodeTransitions(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "alpha", Name: "Альфа", Status: domain.NodeOnline},
		{ID: "beta", Name: "Бета", Status: domain.NodeOnline},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	preferences := state.Preferences[7]
	preferences.Language = domain.LanguageRussian
	state.Preferences[7] = preferences
	port := &clusterLogPort{state: state}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	base := &clusterLogMessenger{}
	activity := telegramoutbound.New(base)
	handler := &Handler{
		service: service, messenger: activity, activity: activity,
		clusterEventLogs: make(map[int64]clusterEventLog),
	}
	leaders := &alertLeader{id: "alpha"}
	tracker := clusterEventTracker{previousLeader: "beta"}
	now := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)

	handler.observeClusterEvents(context.Background(), leaders, "alpha", &tracker, now)
	if len(base.sent) != 1 || !strings.Contains(base.sent[0].Text, "15:00 👑 Альфа") {
		t.Fatalf("leader event=%#v", base.sent)
	}
	node := state.Nodes["beta"]
	node.Status = domain.NodeOffline
	state.Nodes["beta"] = node
	handler.observeClusterEvents(
		context.Background(), leaders, "alpha", &tracker, now.Add(time.Minute),
	)
	node.Status = domain.NodeOnline
	state.Nodes["beta"] = node
	handler.observeClusterEvents(
		context.Background(), leaders, "alpha", &tracker, now.Add(2*time.Minute),
	)
	if len(base.sent) != 1 || len(base.edited) != 2 {
		t.Fatalf("sent=%d edited=%d", len(base.sent), len(base.edited))
	}
	final := base.edited[len(base.edited)-1].Text
	for _, expected := range []string{"15:00 👑 Альфа", "15:01 🔴 Бета", "15:02 🟢 Бета"} {
		if !strings.Contains(final, expected) {
			t.Fatalf("final log %q lacks %q", final, expected)
		}
	}
}

var _ Messenger = (*clusterLogMessenger)(nil)
