package telegramapp_test

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestEveryCardDisplayCombinationPreservesValidSessionNavigation(t *testing.T) {
	cardModes := []domain.ResponseCardMode{
		domain.ResponseCardsKeepPaginated, domain.ResponseCardsKeepLatest,
		domain.ResponseCardsReplace,
	}
	terminalModes := []domain.TerminalSnapshotMode{
		domain.TerminalSnapshotWorking, domain.TerminalSnapshotAlways,
		domain.TerminalSnapshotNever,
	}
	hiddenSets := [][]domain.CardEventType{
		nil,
		{domain.CardEventToolCall},
		{domain.CardEventToolResult},
		{domain.CardEventToolCall, domain.CardEventToolResult, domain.CardEventThinking},
	}
	for _, cardMode := range cardModes {
		for _, terminalMode := range terminalModes {
			for _, hidden := range hiddenSets {
				projector, state, _ := projectorFixture(t)
				unnamed := state.Sessions["alpha/a-old"]
				unnamed.Name = ""
				state.Sessions[unnamed.Ref().Key()] = unnamed
				preferences := state.Preferences[2]
				preferences.ResponseCards = cardMode
				preferences.TerminalSnapshots = terminalMode
				preferences.HiddenCardEvents = hidden
				state.Preferences[2] = preferences
				screen, err := projector.SessionCard(application.Principal{UserID: 2},
					domain.SessionRef{NodeID: "alpha", SessionID: "a-new"})
				if err != nil {
					t.Fatalf("%s/%s/%v: %v", cardMode, terminalMode, hidden, err)
				}
				if err := screen.Validate(); err != nil {
					t.Fatalf("%s/%s/%v: invalid card: %v", cardMode, terminalMode, hidden, err)
				}
				if !strings.Contains(telegramui.CanonicalGrid(screen.Grid), "[… ✅ -> session@s-ao]") {
					t.Fatalf("%s/%s/%v: session switcher disappeared", cardMode, terminalMode, hidden)
				}
			}
		}
	}
}
