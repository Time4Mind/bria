package telegramview_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestNodePickerPaginatesAfterEightServers(t *testing.T) {
	projector, state, tokens := projectorFixture(t)
	for index := 0; index < 7; index++ {
		nodeID := domain.NodeID(fmt.Sprintf("extra-%02d", index))
		if err := state.AddNode(domain.Node{
			ID: nodeID, Name: string(nodeID), Status: domain.NodeOnline,
			CreatedAt: time.Unix(int64(200+index), 0),
		}); err != nil {
			t.Fatal(err)
		}
		state.Users[2].AllowedNodes[nodeID] = true
		tokens.nodes[nodeID] = telegramui.OpaqueToken("n-" + nodeID)
	}
	first, err := projector.OpenSessionsPage(application.Principal{UserID: 2}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grid := telegramui.CanonicalGrid(first.Grid)
	if strings.Count(grid, " -> node@") != 8 || !strings.Contains(grid, "nodes_next@p-2") ||
		!strings.Contains(grid, "nodes_prev@p-2") || !strings.Contains(grid, "1/2") {
		t.Fatalf("first node page=%s", grid)
	}
	second, err := projector.OpenSessionsPage(application.Principal{UserID: 2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	grid = telegramui.CanonicalGrid(second.Grid)
	if strings.Count(grid, " -> node@") != 2 || !strings.Contains(grid, "nodes_prev@p-1") ||
		!strings.Contains(grid, "nodes_next@p-1") || !strings.Contains(grid, "2/2") {
		t.Fatalf("second node page=%s", grid)
	}
}

func TestAllHostsSessionsPaginateOnCompleteThreeButtonRows(t *testing.T) {
	projector, state, tokens := projectorFixture(t)
	preferences := state.Preferences[2]
	preferences.SessionView = domain.ViewAllHosts
	state.Preferences[2] = preferences
	for index := 0; index < 9; index++ {
		id := fmt.Sprintf("extra-session-%02d", index)
		ref := addProjectionSession(t, state, id, "alpha", 2, int64(200+index))
		tokens.sessions[ref.Key()] = telegramui.OpaqueToken(fmt.Sprintf("sx-%02d", index))
	}
	first, err := projector.OpenSessionsPage(application.Principal{UserID: 2}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Grid) != 6 || len(first.Grid[0]) != 3 || len(first.Grid[3]) != 3 ||
		len(first.Grid[5]) != 3 {
		t.Fatalf("first page grid shape=%#v", first.Grid)
	}
	grid := telegramui.CanonicalGrid(first.Grid)
	if strings.Count(grid, " -> session@") != 12 ||
		!strings.Contains(grid, "sessions_prev@p-2") ||
		!strings.Contains(grid, "sessions_next@p-2") {
		t.Fatalf("first page=%s", grid)
	}
	second, err := projector.OpenSessionsPage(application.Principal{UserID: 2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	grid = telegramui.CanonicalGrid(second.Grid)
	if strings.Count(grid, " -> session@") != 2 ||
		!strings.Contains(grid, "sessions_prev@p-1") ||
		!strings.Contains(grid, "sessions_next@p-1") {
		t.Fatalf("second page=%s", grid)
	}
}
