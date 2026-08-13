package telegramapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type alertPort struct {
	machine *clusterstate.Machine
	applies int
}

func (p *alertPort) State() *domain.State { return p.machine.State() }
func (p *alertPort) Apply(
	context.Context,
	clusterstate.Command,
) (clusterstate.Result, error) {
	p.applies++
	return clusterstate.Result{}, errors.New("unexpected replicated write")
}

type alertMessenger struct {
	failNext bool
	sent     []telegramui.Screen
}

func (*alertMessenger) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*alertMessenger) SendTyping(context.Context, int64) error                   { return nil }
func (*alertMessenger) SendDocument(context.Context, telegrambot.DocumentRequest) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (m *alertMessenger) SendScreen(
	_ context.Context,
	_ int64,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	m.sent = append(m.sent, screen)
	if m.failNext {
		m.failNext = false
		return telegrambot.Message{}, errors.New("temporary send failure")
	}
	return telegrambot.Message{ChatID: 7, MessageID: int64(len(m.sent))}, nil
}
func (*alertMessenger) EditScreen(
	context.Context,
	telegrambot.Message,
	telegramui.Screen,
) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (*alertMessenger) DeleteMessage(context.Context, telegrambot.Message) error { return nil }
func (*alertMessenger) ClearKeyboard(context.Context, telegrambot.Message) error { return nil }

type alertLeader struct{ id string }

func (l *alertLeader) LeaderID() string { return l.id }

func TestClusterConnectivityDeliveryIsLocalizedRetriedAndReadOnly(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Альфа"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	preferences := state.Preferences[7]
	preferences.Language = domain.LanguageRussian
	state.Preferences[7] = preferences
	port := &alertPort{machine: clusterstate.NewMachine(state)}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	messenger := &alertMessenger{}
	handler := &Handler{service: service, messenger: messenger}
	leader := &alertLeader{id: "alpha"}
	config := ClusterConnectivityConfig{LocalNodeID: "alpha", LossGrace: time.Second}
	tracker := &connectivityTracker{}
	now := time.Unix(100, 0)

	handler.observeClusterConnectivity(context.Background(), leader, config, tracker, now)
	leader.id = ""
	handler.observeClusterConnectivity(context.Background(), leader, config, tracker, now.Add(time.Second))
	messenger.failNext = true
	handler.observeClusterConnectivity(context.Background(), leader, config, tracker, now.Add(2*time.Second))
	handler.observeClusterConnectivity(context.Background(), leader, config, tracker, now.Add(3*time.Second))
	leader.id = "beta"
	handler.observeClusterConnectivity(context.Background(), leader, config, tracker, now.Add(4*time.Second))

	if len(messenger.sent) != 3 {
		t.Fatalf("send attempts=%d", len(messenger.sent))
	}
	if !strings.Contains(messenger.sent[1].Text, "🔴 Альфа") ||
		!strings.Contains(messenger.sent[2].Text, "🟢 Альфа") {
		t.Fatalf("localized notices=%#v", messenger.sent)
	}
	for _, screen := range messenger.sent {
		if err := screen.Validate(); err != nil {
			t.Fatalf("invalid notice: %v", err)
		}
	}
	if port.applies != 0 {
		t.Fatalf("replicated writes=%d", port.applies)
	}
}
