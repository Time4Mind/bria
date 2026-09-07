package telegramcontroller_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
)

type typedProjectionState struct {
	projectionUIState
	technical map[int]bool
	reads     int
}

func (s *typedProjectionState) LoadCardDisplayHistory(_ context.Context, id domain.SessionID, show bool) ([]string, error) {
	s.reads++
	var result []string
	for index, text := range s.history[id] {
		if show || !s.technical[index] {
			result = append(result, text)
		}
	}
	return result, nil
}

func TestTechnicalDisplayReadsPersistedKindsAfterControllerRestart(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider", 1)
	state := &typedProjectionState{projectionUIState: projectionUIState{history: map[domain.SessionID][]string{ready.ID(): {"commentary", "stored tool", "👨‍💻 user text"}}}, technical: map[int]bool{1: true}}
	settings := &testPreferences{settings: settingsport.Snapshot{CardPageLimit: 64, CardDetail: "standard"}}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: state, Settings: settings})
	defer c.Close(ctx)
	for _, show := range []bool{false, true} {
		settings.settings.ShowTechnicalActions = show
		r, err := c.ProjectCurrent(ctx, ready.ID())
		if err != nil || r.Card == nil || strings.Contains(r.Card.Pages[0].Content, "stored tool") != show || !strings.Contains(r.Card.Pages[0].Content, "commentary") {
			t.Fatalf("persisted kind projection show=%t: %+v %v", show, r, err)
		}
	}
	if state.reads != 2 || len(state.history[ready.ID()]) != 3 {
		t.Fatalf("typed metadata not used or source mutated: %+v", state)
	}
}

func TestTechnicalDisplayToggleFiltersOnlyTypedToolsAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider", 1)
	settings := &testPreferences{settings: settingsport.Snapshot{CardPageLimit: 64, CardDetail: "standard", ShowTechnicalActions: false}}
	state := &projectionUIState{history: make(map[domain.SessionID][]string)}
	done := make(chan struct{}, 1)
	c := newController(t, nil, newLockedSessions(ready), submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
		return sessionruntime.TurnResult{Events: []sessionruntime.TurnEvent{{Kind: sessionruntime.EventKind("tool"), Text: "EXACT_TOOL_PAYLOAD"}, {Kind: sessionruntime.EventCommentary, Text: "🔧 ordinary explanation"}}, Final: "finished", TerminalStatus: sessionruntime.StatusCompleted}, nil
	}), notifierFunc(func(_ context.Context, n telegramcontroller.Notification) error {
		if n.Kind == telegramcontroller.NotificationFinal {
			done <- struct{}{}
		}
		return nil
	}), telegramcontroller.Options{Recovered: []domain.Session{ready}, Settings: settings, UIState: state})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticMessage(ctx, message(990, "🔧 user prompt")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("turn did not finish")
	}
	for _, show := range []bool{false, true, false} {
		settings.settings.ShowTechnicalActions = show
		r, err := c.ProjectCurrent(ctx, ready.ID())
		if err != nil || r.Card == nil {
			t.Fatalf("projection: %+v %v", r, err)
		}
		text := r.Card.Pages[0].Content
		if strings.Contains(text, "EXACT_TOOL_PAYLOAD") != show || !strings.Contains(text, "🔧 ordinary explanation") || !strings.Contains(text, "🔧 user prompt") || !strings.Contains(text, "finished") {
			t.Fatalf("show=%t filtered wrong content: %q", show, text)
		}
		completion, _, err := c.ProjectCompletion(ctx, ready.ID())
		if err != nil || strings.Contains(completion.Pages[0].Content, "EXACT_TOOL_PAYLOAD") != show {
			t.Fatalf("completion ignores tool setting: %+v %v", completion, err)
		}
		legacy, err := c.Handle(ctx, message(991, "/status"))
		if err != nil || strings.Contains(legacy.Status.Text, "EXACT_TOOL_PAYLOAD") != show {
			t.Fatalf("legacy card ignores tool setting: %+v %v", legacy, err)
		}
	}
	if !strings.Contains(strings.Join(state.history[ready.ID()], "\n"), "EXACT_TOOL_PAYLOAD") {
		t.Fatal("display filter erased stored tool history")
	}
}
