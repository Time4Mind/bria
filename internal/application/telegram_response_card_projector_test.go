package application_test

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestKeepLatestResponseCardShowsLastPageWithoutPagination(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	preferences := state.Preferences[2]
	preferences.ResponseCards = domain.ResponseCardsKeepLatest
	state.Preferences[2] = preferences
	events := []application.CardEvent{
		{Kind: application.CardEventAssistantText, Text: "First answer", PageBreak: true},
		{Kind: application.CardEventAssistantText, Text: "Second answer", PageBreak: true},
	}
	screen, err := projector.SessionCardPage(
		application.Principal{UserID: 2},
		domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}, events, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Second answer") || strings.Contains(screen.Text, "First answer") {
		t.Fatalf("card did not select the last page: %q", screen.Text)
	}
	grid := telegramui.CanonicalGrid(screen.Grid)
	if strings.Contains(grid, "page_prev") || strings.Contains(grid, "2/2") {
		t.Fatalf("pagination remained visible: %s", grid)
	}
}
