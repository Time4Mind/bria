package telegramapp_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type refreshUpdateConsensus struct{ machine *clusterstate.Machine }

func (c refreshUpdateConsensus) Apply(
	_ context.Context, command clusterstate.Command,
) (clusterstate.Result, error) {
	return c.machine.Apply(command), nil
}
func (refreshUpdateConsensus) IsLeader() bool                    { return true }
func (refreshUpdateConsensus) LeaderID() string                  { return "allowed" }
func (refreshUpdateConsensus) TransferLeadershipTo(string) error { return nil }

type refreshUpdateNodes struct{}

func (refreshUpdateNodes) Inspect(context.Context) (clusterupdate.VerifiedManifest, error) {
	return clusterupdate.VerifiedManifest{}, nil
}
func (refreshUpdateNodes) Start(
	context.Context, clusterupdate.Request,
) (clusterupdate.Status, error) {
	return clusterupdate.Status{}, nil
}
func (refreshUpdateNodes) Status(
	context.Context, clusterupdate.Request,
) (clusterupdate.Status, error) {
	return clusterupdate.Status{Phase: clusterupdate.PhaseHealthy, Progress: 100}, nil
}

type refreshUpdateMessenger struct {
	mu     sync.Mutex
	edited []telegramui.Screen
	sent   int
	notify chan struct{}
}

func (*refreshUpdateMessenger) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*refreshUpdateMessenger) SendTyping(context.Context, int64) error                   { return nil }
func (*refreshUpdateMessenger) SendDocument(
	context.Context, telegrambot.DocumentRequest,
) (telegrambot.Message, error) {
	return telegrambot.Message{}, nil
}
func (m *refreshUpdateMessenger) SendScreen(
	context.Context, int64, telegramui.Screen,
) (telegrambot.Message, error) {
	m.mu.Lock()
	m.sent++
	m.mu.Unlock()
	return telegrambot.Message{}, nil
}
func (m *refreshUpdateMessenger) EditScreen(
	_ context.Context, message telegrambot.Message, screen telegramui.Screen,
) (telegrambot.Message, error) {
	m.mu.Lock()
	m.edited = append(m.edited, screen)
	m.mu.Unlock()
	message.PaneHash = ""
	message.ScreenHash = "completed"
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return message, nil
}
func (*refreshUpdateMessenger) DeleteMessage(context.Context, telegrambot.Message) error { return nil }
func (*refreshUpdateMessenger) ClearKeyboard(context.Context, telegrambot.Message) error { return nil }

func TestCompletedClusterUpdateCardIsReconciledAfterRestart(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	applyCompletedClusterUpdate(t, fixture.machine)
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "seed-update-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 77, PaneHash: "view:cluster-update", ScreenHash: "stale",
		},
	); err != nil {
		t.Fatal(err)
	}
	coordinator, err := clusterupdate.NewCoordinator(
		"allowed", fixture.machine, refreshUpdateConsensus{machine: fixture.machine}, refreshUpdateNodes{},
	)
	if err != nil {
		t.Fatal(err)
	}
	messenger := &refreshUpdateMessenger{notify: make(chan struct{}, 1)}
	handler, err := telegramapp.NewHandler(
		fixture.service, fixture.projector, fixture.codec, messenger,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.SetClusterUpdater(coordinator); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	waitTestNotification(t, messenger.notify, "completed update card was not restored")
	var card domain.TelegramResponseCard
	var ok bool
	deadline := time.Now().Add(time.Second)
	for {
		card, ok, err = fixture.service.TelegramResponseCard(actor)
		if err == nil && ok && card.PaneHash == "" && card.ScreenHash == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reconciled card=%#v ok=%v err=%v", card, ok, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if messenger.sent != 0 || len(messenger.edited) != 1 {
		t.Fatalf("sent=%d edited=%d", messenger.sent, len(messenger.edited))
	}
	if !strings.Contains(messenger.edited[0].Text, "100%") {
		t.Fatalf("terminal update state is missing: %q", messenger.edited[0].Text)
	}
}

func applyCompletedClusterUpdate(t *testing.T, machine *clusterstate.Machine) {
	t.Helper()
	now := time.Unix(200, 0).UTC()
	commands := []struct {
		kind    clusterstate.CommandKind
		payload any
	}{
		{clusterstate.CommandBeginClusterUpdate, domain.ClusterUpdate{
			ID: "update", Version: "v2",
			ManifestSHA256: strings.Repeat("a", 64), Order: []domain.NodeID{"allowed"},
		}},
		{clusterstate.CommandSetClusterUpdateNode, clusterstate.SetClusterUpdateNode{
			UpdateID: "update", NodeID: "allowed", Phase: domain.NodeUpdateInstalling,
		}},
		{clusterstate.CommandSetClusterUpdateNode, clusterstate.SetClusterUpdateNode{
			UpdateID: "update", NodeID: "allowed", Phase: domain.NodeUpdateHealthy,
		}},
		{clusterstate.CommandFinishClusterUpdate, clusterstate.FinishClusterUpdate{UpdateID: "update"}},
	}
	for index, item := range commands {
		command, err := clusterstate.NewCommand(
			"update-step-"+string(rune('0'+index)), item.kind,
			now.Add(time.Duration(index)*time.Second), item.payload,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := machine.Apply(command).Err(); err != nil {
			t.Fatal(err)
		}
	}
}
