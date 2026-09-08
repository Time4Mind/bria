package telegramcontroller_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
)

func TestTechnicalLinesReachBothTypedProjectionPaths(t *testing.T) {
	for _, persisted := range []bool{true, false} {
		t.Run(fmt.Sprint(persisted), func(t *testing.T) {
			ctx := context.Background()
			ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider", 1)
			var lines []string
			for i := 1; i <= 41; i++ {
				lines = append(lines, fmt.Sprintf("ROW%02d", i))
			}
			tool := cardtranscript.EncodeTool(cardtranscript.Tool{ID: "tool-1", Name: "read", Output: strings.Join(lines, "\n"), Status: "completed"})
			state := &finalPageState{blocks: []cardtranscript.Block{{Kind: "tool", Text: tool}, {Kind: "final", Text: "FINAL"}}}
			prefs := &displayPreferences{testPreferences: testPreferences{settings: settingsport.Snapshot{CardPageLimit: 64, CardDetail: "standard", ShowTechnicalActions: true}}}
			options := telegramcontroller.Options{Recovered: []domain.Session{ready}, Settings: prefs}
			if persisted {
				options.UIState = state
			}
			done := make(chan struct{}, 1)
			c := newController(t, nil, newLockedSessions(ready), submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
				return sessionruntime.TurnResult{Events: []sessionruntime.TurnEvent{{Kind: sessionruntime.EventTool, Text: tool}}, Final: "FINAL", TerminalStatus: sessionruntime.StatusCompleted}, nil
			}), notifierFunc(func(_ context.Context, n telegramcontroller.Notification) error {
				if n.Kind == telegramcontroller.NotificationFinal {
					done <- struct{}{}
				}
				return nil
			}), options)
			defer c.Close(ctx)
			if !persisted {
				if _, err := c.HandleSemanticMessage(ctx, message(900, "PROMPT")); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("turn timeout")
				}
			}
			for _, limit := range []int{5, 10, 20, 40, 0} {
				prefs.mu.Lock()
				prefs.settings.TechnicalOutputLines = limit
				prefs.mu.Unlock()
				want := limit
				if want == 0 {
					want = 10
				}
				current, err := c.ProjectCurrent(ctx, ready.ID())
				if err != nil || current.Card == nil {
					t.Fatalf("current: %v", err)
				}
				completion, _, err := c.ProjectCompletion(ctx, ready.ID())
				if err != nil {
					t.Fatal(err)
				}
				for name, card := range map[string]telegramcontroller.SemanticCard{"current": *current.Card, "completion": completion} {
					var text string
					for _, page := range card.Pages {
						text += page.Content
					}
					if !strings.Contains(text, fmt.Sprintf("ROW%02d", want)) || strings.Contains(text, fmt.Sprintf("ROW%02d", want+1)) || !strings.Contains(text, "FINAL") {
						t.Errorf("%s limit=%d ignored: %s", name, limit, text)
					}
				}
			}
			if state.blocks[0].ToolLines != 0 || state.blocks[0].Text != tool {
				t.Fatal("projection mutated persisted block")
			}
		})
	}
}
