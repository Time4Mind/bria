package application_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type projectionReader struct{ state *domain.State }

func (r projectionReader) State() *domain.State { return r.state.Clone() }

type projectionTokens struct {
	nodes    map[domain.NodeID]telegramui.OpaqueToken
	sessions map[string]telegramui.OpaqueToken
	calls    []string
}

func (t *projectionTokens) Node(
	actor domain.UserID,
	action telegramui.Action,
	nodeID domain.NodeID,
) (telegramui.OpaqueToken, error) {
	if actor != 2 || !allowedNodeTokenAction(action) {
		return "", errors.New("node token was not actor/action scoped")
	}
	t.calls = append(t.calls, "node:"+string(nodeID))
	return t.nodes[nodeID], nil
}

func allowedNodeTokenAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionSelectNode, telegramui.ActionSelectArchiveNode,
		telegramui.ActionStatusLeaderNode, telegramui.ActionStatusSettingsNode,
		telegramui.ActionNodeSettings, telegramui.ActionNewSession, telegramui.ActionConfirmLeader,
		telegramui.ActionSetLeaderNode:
		return true
	default:
		return false
	}
}

func (t *projectionTokens) Session(
	actor domain.UserID,
	action telegramui.Action,
	ref domain.SessionRef,
) (telegramui.OpaqueToken, error) {
	if actor != 2 || !allowedSessionTokenAction(action) {
		return "", errors.New("session token was not actor/action scoped")
	}
	t.calls = append(t.calls, string(action)+":"+ref.Key())
	return t.sessions[ref.Key()], nil
}

func allowedSessionTokenAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionSelectSession, telegramui.ActionSelectArchive,
		telegramui.ActionPagePrevious, telegramui.ActionPageLatest, telegramui.ActionPageNext,
		telegramui.ActionStop, telegramui.ActionClose, telegramui.ActionClear,
		telegramui.ActionTerminal, telegramui.ActionRestore:
		return true
	default:
		return false
	}
}

func projectorFixture(t *testing.T) (*application.TelegramProjector, *domain.State, *projectionTokens) {
	t.Helper()
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline},
		{ID: "beta", Name: "Beta", Status: domain.NodeOffline},
		{ID: "gamma", Name: "Gamma", Status: domain.NodeOnline},
		{ID: "secret", Name: "Secret", Status: domain.NodeOnline},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "alpha", "beta", "gamma", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(2, domain.RoleMember, "alpha", "beta", "gamma"); err != nil {
		t.Fatal(err)
	}
	addProjectionSession(t, state, "a-old", "alpha", 2, 10)
	addProjectionSession(t, state, "private", "alpha", 1, 15)
	addProjectionSession(t, state, "g-old", "gamma", 2, 18)
	addProjectionSession(t, state, "a-new", "alpha", 2, 20)
	shared := addProjectionSession(t, state, "shared", "alpha", 1, 30)
	if err := state.ShareSession(1, shared, 2, domain.ShareView); err != nil {
		t.Fatal(err)
	}
	addProjectionSession(t, state, "b-live", "beta", 2, 35)
	addProjectionSession(t, state, "g-new", "gamma", 2, 40)
	addProjectionSession(t, state, "hidden", "secret", 1, 5)

	archiveSession(t, state, "a-archive-old", "alpha", 1, 50, 90, true)
	archiveSession(t, state, "a-archive-new", "alpha", 2, 60, 100, false)
	archiveSession(t, state, "b-archive", "beta", 2, 60, 110, false)
	archiveSession(t, state, "secret-archive", "secret", 1, 60, 120, false)

	state.Navigation.ActiveNodeByUser[2] = "alpha"
	state.Navigation.ActiveSessionByUserNode[2] = map[domain.NodeID]domain.SessionID{
		"alpha": "a-new",
		"beta":  "b-live",
	}
	tokens := &projectionTokens{
		nodes: map[domain.NodeID]telegramui.OpaqueToken{
			"alpha": "n-alpha", "beta": "n-beta", "gamma": "n-gamma",
		},
		sessions: map[string]telegramui.OpaqueToken{
			"alpha/a-old": "s-ao", "alpha/a-new": "s-an", "alpha/shared": "s-as",
			"gamma/g-old": "s-go", "gamma/g-new": "s-gn",
			"beta/b-live":         "s-bl",
			"alpha/a-archive-old": "a-old", "alpha/a-archive-new": "a-new",
			"beta/b-archive": "b-archive",
		},
	}
	projector, err := application.NewTelegramProjector(projectionReader{state}, tokens)
	if err != nil {
		t.Fatal(err)
	}
	return projector, state, tokens
}

func addProjectionSession(
	t *testing.T,
	state *domain.State,
	id, node string,
	owner domain.UserID,
	created int64,
) domain.SessionRef {
	t.Helper()
	timestamp := time.Unix(created, 0).UTC()
	session := domain.Session{
		ID: domain.SessionID(id), NodeID: domain.NodeID(node), OwnerID: owner,
		Name: id, Backend: "claude", State: domain.SessionActive,
		CreatedAt: timestamp, LiveSinceAt: timestamp, LastEventAt: timestamp,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	return session.Ref()
}

func archiveSession(
	t *testing.T,
	state *domain.State,
	id, node string,
	owner domain.UserID,
	created, archived int64,
	share bool,
) {
	t.Helper()
	ref := addProjectionSession(t, state, id, node, owner, created)
	session := state.Sessions[ref.Key()]
	session.State = domain.SessionArchived
	session.ArchivedAt = time.Unix(archived, 0).UTC()
	state.Sessions[ref.Key()] = session
	if share {
		if err := state.ShareSession(owner, ref, 2, domain.ShareView); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDefaultHostFirstEntryProjectsActorFilteredNodeSelectorGolden(t *testing.T) {
	projector, _, tokens := projectorFixture(t)

	screen, err := projector.OpenSessions(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, screen, `[🟢 ✓ Alpha (3) -> node@n-alpha]
[🔴 Beta · unavailable -> node@n-beta]
[🟢 Gamma (2) -> node@n-gamma]
[← Back -> sessions]`)
	assertNoTokenCall(t, tokens.calls, "secret")
}

func TestHostFirstNodeSessionsPreserveStableOrderingGolden(t *testing.T) {
	projector, _, tokens := projectorFixture(t)

	screen, err := projector.NodeSessionsPageWithContext(
		application.Principal{UserID: 2}, "alpha", 1,
		map[string]int{"alpha/a-old": 21, "alpha/a-new": 34, "alpha/shared": 55},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, screen, `[a-old 🟢 · 21% -> session@s-ao] | [✓ a-new 🟢 · 34% -> session@s-an] | [shared 🟢 · 55% -> session@s-as]
[🆕 New -> new@n-alpha] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
	assertNoTokenCall(t, tokens.calls, "private")
}

func TestExplicitNodeSelectorDoesNotCollapseSingleVisibleNode(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	if err := state.SetNodeAccess(2, domain.RoleMember, "alpha"); err != nil {
		t.Fatal(err)
	}
	screen, err := projector.OpenNodeSelector(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if grid := telegramui.CanonicalGrid(screen.Grid); !strings.Contains(grid, "Alpha") ||
		strings.Contains(screen.Text, "active sessions") {
		t.Fatalf("explicit server selector collapsed into sessions: text=%q grid=%s", screen.Text, grid)
	}
}

func TestAllHostsGridExcludesOfflineAndUnauthorizedSessionsGolden(t *testing.T) {
	projector, state, tokens := projectorFixture(t)
	preferences := state.Preferences[2]
	preferences.SessionView = domain.ViewAllHosts
	state.Preferences[2] = preferences

	screen, err := projector.OpenSessionsPageWithContext(
		application.Principal{UserID: 2}, 1,
		map[string]int{"alpha/a-old": 21, "gamma/g-new": 13},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, screen, `[🟥 a-old · Alpha 🟢 · 21% -> session@s-ao] | [🟦 g-old · Gamma 🟢 -> session@s-go] | [✓ 🟥 a-new · Alpha 🟢 -> session@s-an]
[🟥 shared · Alpha 🟢 -> session@s-as] | [🟦 g-new · Gamma 🟢 · 13% -> session@s-gn]
[🆕 New -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
	if len(screen.Grid[0]) != 3 {
		t.Fatalf("first all-host row = %d, want 3", len(screen.Grid[0]))
	}
	for _, forbidden := range []string{"beta/b-live", "secret", "private"} {
		assertNoTokenCall(t, tokens.calls, forbidden)
	}
}

func TestOfflineNodeOffersLastCardAndArchiveWithoutLiveList(t *testing.T) {
	projector, _, _ := projectorFixture(t)

	screen, err := projector.NodeSessions(application.Principal{UserID: 2}, "beta")
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, screen, `[Last card · b-live -> session@s-bl]
[🗄 Archive -> archive]
[← Servers -> sessions@servers]`)
}

func TestProjectionRejectsUnknownActorBeforeReadingEntities(t *testing.T) {
	projector, _, tokens := projectorFixture(t)
	_, err := projector.OpenSessions(application.Principal{UserID: 99})
	if !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("unknown actor error = %v", err)
	}
	if len(tokens.calls) != 0 {
		t.Fatalf("tokenized entities before actor authorization: %v", tokens.calls)
	}
}

func assertProjectionGolden(t *testing.T, screen telegramui.Screen, want string) {
	t.Helper()
	if err := screen.Validate(); err != nil {
		t.Fatalf("invalid screen: %v", err)
	}
	if got := telegramui.CanonicalGrid(screen.Grid); got != want {
		t.Fatalf("grid mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func assertNoTokenCall(t *testing.T, calls []string, fragment string) {
	t.Helper()
	for _, call := range calls {
		if strings.Contains(call, fragment) {
			t.Fatalf("unauthorized/offline entity reached token projection: %q", call)
		}
	}
}
