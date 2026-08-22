package telegramview_test

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestWaitingPromptShowsNeedsInputStateInSessionSelectors(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	session := state.Sessions["alpha/a-old"]
	session.RuntimePhase = domain.RuntimeWaitingInput
	session.InteractivePrompt = &domain.InteractivePrompt{
		Kind: "question", Hash: "0123456789abcdef0123456789abcdef",
	}
	state.Sessions[session.Ref().Key()] = session

	nodeScreen, err := projector.NodeSessions(application.Principal{UserID: 2}, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if grid := telegramui.CanonicalGrid(nodeScreen.Grid); !strings.Contains(grid, "[a-old ❓ ->") {
		t.Fatalf("node grid=%s", grid)
	}
	preferences := state.Preferences[2]
	preferences.SessionView = domain.ViewAllHosts
	state.Preferences[2] = preferences
	allScreen, err := projector.OpenSessions(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if grid := telegramui.CanonicalGrid(allScreen.Grid); !strings.Contains(grid, "🟥 a-old · Alpha ❓") {
		t.Fatalf("all-host grid=%s", grid)
	}
}
