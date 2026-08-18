package telegramapp_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/workspace"
)

type createStarterStub struct {
	events      *[]string
	candidates  []sessionstart.ProviderCandidate
	offsets     *[]int
	directories []workspace.Directory
}

func (s createStarterStub) Browse(_ context.Context, _ application.Principal, _ domain.NodeID, _ string) (sessionstart.BrowseResult, error) {
	*s.events = append(*s.events, "browse")
	directories := s.directories
	if directories == nil {
		directories = []workspace.Directory{{Name: "project", Path: "/home/test/project"}}
	}
	return sessionstart.BrowseResult{Path: "/home/test", Directories: directories}, nil
}
func (s createStarterStub) Discover(_ context.Context, _ application.Principal, _ domain.NodeID, _ string, _ string, offset, limit int) (sessionstart.ProviderPage, error) {
	*s.events = append(*s.events, "discover")
	if s.offsets != nil {
		*s.offsets = append(*s.offsets, offset)
	}
	if offset >= len(s.candidates) {
		return sessionstart.ProviderPage{Total: len(s.candidates)}, nil
	}
	end := min(len(s.candidates), offset+limit)
	return sessionstart.ProviderPage{Items: append([]sessionstart.ProviderCandidate(nil), s.candidates[offset:end]...), Total: len(s.candidates)}, nil
}
func (s createStarterStub) Create(context.Context, application.Principal, application.CreateSessionRequest) (domain.Session, error) {
	*s.events = append(*s.events, "create")
	return domain.Session{}, nil
}

func TestAllHostsNewSessionSelectsServerAndAnswersBeforeBrowse(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	preferences := fixture.machine.State().Preferences[7]
	preferences.SessionView = domain.ViewAllHosts
	result := fixture.machine.Apply(commandForTest(t, "all-hosts", clusterstate.CommandSetPreferences, clusterstate.SetPreferences{UserID: 7, Preferences: preferences}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	newData, err := (telegramui.Callback{Action: telegramui.ActionNewSession}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{UpdateID: 70, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7, CallbackID: "new", CallbackData: newData, CallbackOrigin: origin}); err != nil {
		t.Fatal(err)
	}
	serverScreen := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if serverScreen.Name != telegramui.ScreenNodes || serverScreen.Grid[0][0].Callback.Action != telegramui.ActionNewNode {
		t.Fatalf("server selector=%#v", serverScreen)
	}
	actor := application.Principal{UserID: 7}
	card, exists, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !exists || card.Session != (domain.SessionRef{}) {
		t.Fatalf("create navigation card=%#v exists=%v err=%v", card, exists, cardErr)
	}
	*fixture.events = (*fixture.events)[:0]
	nodeData, err := serverScreen.Grid[0][0].Callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{UpdateID: 71, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7, CallbackID: "node", CallbackData: nodeData, CallbackOrigin: origin}); err != nil {
		t.Fatal(err)
	}
	if got := *fixture.events; len(got) < 3 || got[0] != "answer" || got[1] != "browse" || got[2] != "edit" {
		t.Fatalf("event order=%v", got)
	}
	directoryScreen := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if directoryScreen.Grid[0][0].Callback.Action != telegramui.ActionNewDirectory {
		t.Fatalf("directory browser=%#v", directoryScreen)
	}
}

func TestHostFirstNewSessionReusesSelectedNode(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	*fixture.events = (*fixture.events)[:0]
	data, err := (telegramui.Callback{Action: telegramui.ActionNewSession}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{UpdateID: 72, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7, CallbackID: "new", CallbackData: data, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10}}); err != nil {
		t.Fatal(err)
	}
	if got, want := *fixture.events, []string{"answer", "apply", "browse", "edit", "apply"}; !slices.Equal(got, want) {
		t.Fatalf("event order=%v", got)
	}
	screen := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if screen.Name == telegramui.ScreenNodes || screen.Grid[0][0].Callback.Action != telegramui.ActionNewDirectory {
		t.Fatalf("host-first creation did not open directory browser: %#v", screen)
	}
}

func TestNewSessionFromNodeSessionsUsesThatNode(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	sessions, err := fixture.projector.NodeSessions(application.Principal{UserID: 7}, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	var create telegramui.Callback
	for _, row := range sessions.Grid {
		for _, button := range row {
			if button.Callback.Action == telegramui.ActionNewSession {
				create = button.Callback
			}
		}
	}
	if create.Token == "" {
		t.Fatalf("node session screen has no node-scoped new button: %#v", sessions.Grid)
	}
	data, err := create.Encode()
	if err != nil {
		t.Fatal(err)
	}
	*fixture.events = (*fixture.events)[:0]
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{UpdateID: 74, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7, CallbackID: "node-new", CallbackData: data, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10}}); err != nil {
		t.Fatal(err)
	}
	if got, want := *fixture.events, []string{"answer", "apply", "browse", "edit", "apply"}; !slices.Equal(got, want) {
		t.Fatalf("direct node creation events=%v", got)
	}
	screen := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if screen.Name == telegramui.ScreenNodes {
		t.Fatalf("node-scoped create unexpectedly opened server selector: %#v", screen)
	}
}

func TestNewSessionConnectsInstalledBackendAndContinuesToDirectories(t *testing.T) {
	fixture := newFixture(t)
	result := fixture.machine.Apply(commandForTest(t, "installed-setup-backend", clusterstate.CommandPublishNodeHeartbeat, clusterstate.PublishNodeHeartbeat{NodeID: "allowed", BootID: "boot-setup", Backends: []domain.BackendDescriptor{{Name: "codex", Capabilities: []string{"session.create"}}}}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	sessions, err := fixture.projector.NodeSessions(application.Principal{UserID: 7}, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	newData, err := (telegramui.Callback{Action: telegramui.ActionNewSession, Token: actionToken(t, sessions, telegramui.ActionNewSession)}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{UpdateID: 76, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7, CallbackID: "setup-new", CallbackData: newData, CallbackOrigin: origin}); err != nil {
		t.Fatal(err)
	}
	setup := lastEdited(t, fixture)
	connect := actionToken(t, setup, telegramui.ActionBackendConnect)
	connectData, err := (telegramui.Callback{Action: telegramui.ActionBackendConnect, Token: connect}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{UpdateID: 77, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7, CallbackID: "setup-connect", CallbackData: connectData, CallbackOrigin: origin}); err != nil {
		t.Fatal(err)
	}
	continued := lastEdited(t, fixture)
	if continued.Grid[0][0].Callback.Action != telegramui.ActionNewDirectory || !slices.Contains(*fixture.events, "browse") {
		t.Fatalf("setup did not continue creation: %#v events=%v", continued, *fixture.events)
	}
}

func invokeCreateAction(t *testing.T, fixture fixture, updateID int64, origin telegrambot.Message, action telegramui.Action, token telegramui.OpaqueToken) {
	t.Helper()
	data, err := (telegramui.Callback{Action: action, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{UpdateID: updateID, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7, CallbackID: "create", CallbackData: data, CallbackOrigin: origin}); err != nil {
		t.Fatal(err)
	}
}
func actionToken(t *testing.T, screen telegramui.Screen, action telegramui.Action) telegramui.OpaqueToken {
	t.Helper()
	for _, row := range screen.Grid {
		for _, item := range row {
			if item.Callback.Action == action {
				return item.Callback.Token
			}
		}
	}
	t.Fatalf("action %q not found in %#v", action, screen.Grid)
	return ""
}
func enableCreateBackend(t *testing.T, fixture fixture) {
	t.Helper()
	result := fixture.machine.Apply(commandForTest(t, "backend", clusterstate.CommandUpdateNodeRuntime, clusterstate.UpdateNodeRuntime{NodeID: "allowed", Status: domain.NodeOnline, Backends: []domain.BackendDescriptor{{Name: "codex", Capabilities: []string{"session.create"}}}}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
}
func commandForTest(t *testing.T, id string, kind clusterstate.CommandKind, payload any) clusterstate.Command {
	t.Helper()
	command, err := clusterstate.NewCommand(id, kind, fixtureTime(), payload)
	if err != nil {
		t.Fatal(err)
	}
	return command
}
func fixtureTime() time.Time { return time.Unix(500, 0).UTC() }
