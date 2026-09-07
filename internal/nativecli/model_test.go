package nativecli

import (
	"bria/internal/domain"
	"context"
	"strings"
	"testing"
)

func TestStartupBusyFooterDoesNotAdmitHistoricalComposer(t *testing.T) {
	for _, footer := range []string{
		"Starting MCP servers (0/2): codex_apps, openaiDeveloperDocs (0s • esc to interrupt)",
		"tab to queue message",
		"Working (2s • esc to interrupt)",
	} {
		screen := "› /model\nprevious model menu output\n\n" + footer
		if state := ParseScreen(screen); state.Ready {
			t.Errorf("busy screen accepted historical composer: %q", footer)
		}
	}
	if state := ParseScreen("old output: esc to interrupt\n›\n? for shortcuts"); !state.Ready {
		t.Fatal("old busy text blocked a new idle composer")
	}
}

func TestHistoryBeforeComposerMountIsNotReady(t *testing.T) {
	for _, screen := range []string{
		"› /model\n" + strings.Repeat("historical assistant output\n", 12),
		"› ordinary prior prompt\nassistant response",
		"› /model\n? for shortcuts",
		"›\n" + strings.Repeat("historical output\n", 6),
	} {
		if ParseScreen(screen).Ready {
			t.Errorf("historical/draft composer admitted: %q", screen)
		}
	}
	for _, screen := range []string{"›\n? for shortcuts", "old answer\n› Ask anything\n100% context left", "───\n❯\n───\n⏵⏵ bypass permissions on (shift+tab to cycle)"} {
		if !ParseScreen(screen).Ready {
			t.Errorf("idle composer rejected: %q", screen)
		}
	}
}

type startupTerminal struct {
	fakeTerminal
	inputCaptures []int
}

func (terminal *startupTerminal) Input(ctx context.Context, text string) error {
	terminal.inputCaptures = append(terminal.inputCaptures, terminal.captures)
	return terminal.fakeTerminal.Input(ctx, text)
}

func TestReadyWaitsForIdleBeforeNativeStatus(t *testing.T) {
	terminal := &startupTerminal{fakeTerminal: fakeTerminal{screens: []string{
		"› /model\n" + strings.Repeat("historical assistant output\n", 12),
		"› /model\nStarting MCP servers (0/2): codex_apps, openaiDeveloperDocs (0s • esc to interrupt)\ntab to queue message",
		"›\n? for shortcuts",
		"Session: " + testID,
	}}}
	if _, err := Ready(context.Background(), terminal, Plan{Provider: domain.ProviderCodex}); err != nil {
		t.Fatal(err)
	}
	if len(terminal.inputCaptures) != 1 || terminal.inputCaptures[0] != 3 {
		t.Fatalf("/status injected at captures=%v", terminal.inputCaptures)
	}
}

func TestScreenModelUsesCommittedSelectionNotCursor(t *testing.T) {
	// Labels/markers characterized in installed Codex strings and Claude
	// 2.1.251 strings; compact Codex footer is covered by legacy fixtures.
	for _, test := range []struct {
		text, want  string
		interactive bool
	}{
		{"Select Model and Effort\n› 1. gpt-new\n  2. gpt-old (current)\nPress Enter to select", "gpt-old", true},
		{"Select Model\n› 1. gpt-new\n  2. gpt-old\nPress Enter to select", "", true},
		{"Select model\n❯ 1. Opus 5\n  2. Sonnet 4.6 (current)\nEnter to select", "Sonnet 4.6", true},
		{"Model: gpt-old\nModel: gpt-new (high)", "gpt-new", false},
		{"Current model: claude-sonnet-4-6", "claude-sonnet-4-6", false},
		{"⎿ Set model to Opus 5 (default)", "Opus 5", false},
		{"Kept model as Sonnet 4.6", "Sonnet 4.6", false},
		{"Select model\nCurrently using claude-opus-5 for this session.", "claude-opus-5", true},
		{"›\ngpt-5.6-sol xhigh · ~/bria", "gpt-5.6-sol", false},
		{"Claude Code v2.1.251\nSonnet 4.6 · Claude Max\n❯", "Sonnet 4.6", false},
		{"I recommend Opus 5 for this task\n›", "", false},
	} {
		state := ParseScreen(test.text)
		if state.Model != test.want || state.Interactive != test.interactive {
			t.Errorf("%q => %#v want %q interactive=%v", test.text, state, test.want, test.interactive)
		}
		if test.interactive && state.Ready {
			t.Error("picker cursor identified as input ready")
		}
	}
}
