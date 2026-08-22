package telegramapp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (t *projectionTokens) Choice(
	actor domain.UserID,
	action telegramui.Action,
	flowID string,
	value string,
) (telegramui.OpaqueToken, error) {
	if actor != 2 || !validProjectionChoice(action, flowID) {
		return "", errors.New("choice token was not actor/action scoped")
	}
	return telegramui.OpaqueToken("p-" + value), nil
}

func validProjectionChoice(action telegramui.Action, flowID string) bool {
	switch action {
	case telegramui.ActionArchivePrevious, telegramui.ActionArchiveNext:
		return flowID == "archive-page"
	case telegramui.ActionNodesPrevious, telegramui.ActionNodesNext:
		return flowID == "nodes-page"
	case telegramui.ActionSessionsPrevious, telegramui.ActionSessionsNext:
		return strings.HasPrefix(flowID, "sessions-page-")
	default:
		return false
	}
}

func TestArchiveWithoutSelectedNodeUsesServerPicker(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	lost := addProjectionSession(t, state, "lost", "beta", 2, 130)
	lostSession := state.Sessions[lost.Key()]
	lostSession.State = domain.SessionLost
	state.Sessions[lost.Key()] = lostSession
	state.Navigation.ActiveNodeByUser[2] = ""
	screen, err := projector.OpenArchives(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, screen, `[🟢 Alpha · 2 archived -> archive_node@n-alpha]
[🔴 Beta · 1 archived -> archive_node@n-beta]
[🟢 Gamma · 0 archived -> archive_node@n-gamma]
[← Back -> menu]`)
}

func TestArchivesAreScopedToSelectedAuthorizedNodeEvenWhenOffline(t *testing.T) {
	projector, state, tokens := projectorFixture(t)

	selected, err := projector.SelectedNodeArchives(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, selected, `[1. a-archive-new -> archive_item@a-new] | [2. a-archive-old -> archive_item@a-old]
[1/1 -> noop]
[← Back -> menu]`)
	offline, err := projector.NodeArchives(application.Principal{UserID: 2}, "beta")
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, offline, `[1. b-archive -> archive_item@b-archive]
[1/1 -> noop]
[← Back -> menu]`)
	if _, err := projector.NodeArchives(application.Principal{UserID: 2}, "secret"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("secret archive error = %v, want not found", err)
	}
	assertNoTokenCall(t, tokens.calls, "secret")
	state.Navigation.ActiveNodeByUser[2] = "secret"
	if _, err := projector.SelectedNodeArchives(application.Principal{UserID: 2}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unauthorized selected node error = %v", err)
	}
}

func TestAllHostsArchiveIsOneNewestFirstListWithNodeLabels(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	lost := addProjectionSession(t, state, "lost", "beta", 2, 130)
	lostSession := state.Sessions[lost.Key()]
	lostSession.State = domain.SessionLost
	state.Sessions[lost.Key()] = lostSession
	preferences := state.Preferences[2]
	preferences.SessionView = domain.ViewAllHosts
	state.Preferences[2] = preferences
	screen, err := projector.OpenArchives(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertProjectionGolden(t, screen, `[1. b-archive · Beta -> archive_item@b-archive] | [2. a-archive-new · Alpha -> archive_item@a-new]
[3. a-archive-old · Alpha -> archive_item@a-old]
[1/1 -> noop]
[← Back -> menu]`)
	if strings.Contains(screen.Text, "lost") {
		t.Fatalf("lost session appeared in archive: %q", screen.Text)
	}
}

func TestArchiveListPageTracksCurrentNewestFirstPosition(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	preferences := state.Preferences[2]
	preferences.SessionView = domain.ViewAllHosts
	state.Preferences[2] = preferences
	for index, id := range []string{"new-1", "new-2", "new-3", "new-4", "new-5"} {
		at := int64(200 + index)
		archiveSession(t, state, id, "gamma", 2, at, at, false)
	}
	page, err := projector.ArchiveListPage(application.Principal{UserID: 2}, domain.SessionRef{
		NodeID: "alpha", SessionID: "a-archive-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page != 2 {
		t.Fatalf("archive page=%d, want 2", page)
	}
}
