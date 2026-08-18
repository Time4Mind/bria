package telegramapp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/workspace"
)

func TestNewSessionResumeCandidatesArePaged(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	candidates := make([]sessionstart.ProviderCandidate, 10)
	for index := range candidates {
		candidates[index] = sessionstart.ProviderCandidate{ID: fmt.Sprintf("provider-%d", index+1), Summary: fmt.Sprintf("candidate %d", index+1)}
	}
	var offsets []int
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events, candidates: candidates, offsets: &offsets}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateAction(t, fixture, 80, origin, telegramui.ActionNewSession, "")
	directories := lastEdited(t, fixture)
	invokeCreateAction(t, fixture, 81, origin, telegramui.ActionNewDirectoryPick, actionToken(t, directories, telegramui.ActionNewDirectoryPick))
	first := lastEdited(t, fixture)
	grid := telegramui.CanonicalGrid(first.Grid)
	if !strings.Contains(grid, "1 · candidate 1") || !strings.Contains(grid, "8 · candidate 8") || !strings.Contains(grid, "1/2") || strings.Contains(grid, "9 · candidate 9") {
		t.Fatalf("first resume page=%s", grid)
	}
	invokeCreateAction(t, fixture, 82, origin, telegramui.ActionNewResumeNext, "")
	second := lastEdited(t, fixture)
	grid = telegramui.CanonicalGrid(second.Grid)
	if !strings.Contains(grid, "9 · candidate 9") || !strings.Contains(grid, "10 · candidate 10") || !strings.Contains(grid, "2/2") {
		t.Fatalf("second resume page=%s", grid)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 8 {
		t.Fatalf("discovery offsets=%v", offsets)
	}
}

func TestNewSessionResumePaginationWrapsAndCounterReturnsFirst(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	candidates := make([]sessionstart.ProviderCandidate, 10)
	for index := range candidates {
		candidates[index] = sessionstart.ProviderCandidate{ID: fmt.Sprintf("provider-%d", index+1), Summary: fmt.Sprintf("candidate %d", index+1)}
	}
	var offsets []int
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events, candidates: candidates, offsets: &offsets}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateAction(t, fixture, 85, origin, telegramui.ActionNewSession, "")
	invokeCreateAction(t, fixture, 86, origin, telegramui.ActionNewDirectoryPick, actionToken(t, lastEdited(t, fixture), telegramui.ActionNewDirectoryPick))
	invokeCreateAction(t, fixture, 87, origin, telegramui.ActionNewResumePrevious, "")
	if grid := telegramui.CanonicalGrid(lastEdited(t, fixture).Grid); !strings.Contains(grid, "2/2") {
		t.Fatalf("previous did not wrap to last page: %s", grid)
	}
	invokeCreateAction(t, fixture, 88, origin, telegramui.ActionNewResumeFirst, "")
	if grid := telegramui.CanonicalGrid(lastEdited(t, fixture).Grid); !strings.Contains(grid, "1/2") {
		t.Fatalf("counter did not return to first page: %s", grid)
	}
	if got := fmt.Sprint(offsets); got != "[0 8 0]" {
		t.Fatalf("discovery offsets=%s", got)
	}
}

func TestNewSessionDirectoryPaginationWrapsAndHasCancel(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	directories := make([]workspace.Directory, 7)
	for index := range directories {
		directories[index] = workspace.Directory{Name: fmt.Sprintf("project-%d", index+1), Path: fmt.Sprintf("/home/test/project-%d", index+1)}
	}
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events, directories: directories}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateAction(t, fixture, 89, origin, telegramui.ActionNewSession, "")
	first := lastEdited(t, fixture)
	grid := telegramui.CanonicalGrid(first.Grid)
	if !strings.Contains(grid, "[.. -> new_up] | [Select -> new_pick] | [≡ Menu -> menu]\n[Cancel -> sessions]") || !strings.Contains(grid, "[1/2 -> new_dfirst]") {
		t.Fatalf("directory controls=%s", grid)
	}
	invokeCreateAction(t, fixture, 90, origin, telegramui.ActionNewDirectoryPrev, "")
	if grid = telegramui.CanonicalGrid(lastEdited(t, fixture).Grid); !strings.Contains(grid, "2/2") || !strings.Contains(grid, "project-7") {
		t.Fatalf("directory previous did not wrap: %s", grid)
	}
	invokeCreateAction(t, fixture, 91, origin, telegramui.ActionNewDirectoryFirst, "")
	if grid = telegramui.CanonicalGrid(lastEdited(t, fixture).Grid); !strings.Contains(grid, "1/2") || strings.Contains(grid, "project-7") {
		t.Fatalf("directory counter did not return first: %s", grid)
	}
}

func TestNewSessionCanSkipResumeDiscovery(t *testing.T) {
	fixture := newFixture(t)
	enableCreateBackend(t, fixture)
	preferences := fixture.machine.State().Preferences[7]
	preferences.SkipResumeSelection = true
	if err := fixture.service.SetPreferences(context.Background(), application.Principal{UserID: 7}, preferences); err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10}
	invokeCreateAction(t, fixture, 83, origin, telegramui.ActionNewSession, "")
	invokeCreateAction(t, fixture, 84, origin, telegramui.ActionNewDirectoryPick, actionToken(t, lastEdited(t, fixture), telegramui.ActionNewDirectoryPick))
	if strings.Contains(strings.Join(*fixture.events, ","), "discover") {
		t.Fatalf("resume discovery was not skipped: %v", *fixture.events)
	}
	if !strings.Contains(strings.Join(*fixture.events, ","), "create") {
		t.Fatalf("fresh creation did not start: %v", *fixture.events)
	}
}
