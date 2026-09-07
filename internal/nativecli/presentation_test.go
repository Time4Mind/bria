package nativecli

import (
	"strings"
	"testing"
)

func TestInteractivePresentationExcludesOldStatusAndClosedPickers(t *testing.T) {
	status := "Account: private@example.invalid\nSession: " + testID + "\nModel: gpt-old\n\n"
	picker := "│ Select Model and Effort │\n│ › 1. gpt-new │\n│     Fast model │\n│   2. gpt-old (current) │\n│ Press enter to confirm or esc to go back │"
	state := ParseScreen(status + picker)
	if !state.Interactive || strings.Contains(state.Content, "Account:") || strings.Contains(state.Content, "Session:") || strings.Contains(state.Content, "│") || !strings.Contains(state.Content, "─────") || !strings.Contains(state.Content, "Fast model") {
		t.Fatalf("wrong active region: %+v", state)
	}
	closed := ParseScreen(status + picker + "\n\n›\ngpt-new high · /work")
	if closed.Interactive || closed.Content != "" || !closed.Ready {
		t.Fatalf("old picker still active after composer returned: %+v", closed)
	}
}

func TestInteractivePresentationKeepsLastPickerAndBoundsUnicode(t *testing.T) {
	state := ParseScreen("Select model\n› 1. old\nEnter to select\n\nSelect Reasoning Level\n› 1. low\n  2. high\n" + strings.Repeat("Ж", 4200) + "\nPress enter to confirm or esc to go back")
	if !state.Interactive || len([]rune(state.Content)) > 3600 || strings.Contains(state.Content, "1. old") || !strings.HasSuffix(state.Content, "Press enter to confirm or esc to go back") {
		t.Fatalf("active picker crop invalid: len=%d %+v", len([]rune(state.Content)), state.Interactive)
	}
}

func TestGenericNumberedQuestionRequiresNativeSelectionFooter(t *testing.T) {
	question := "Which environment?\n❯ 1. local\n  2. remote"
	state := ParseScreen(question + "\nEnter to select · Esc to cancel")
	if !state.Interactive || !strings.Contains(state.Content, "Which environment?") {
		t.Fatalf("generic question picker missing: %+v", state)
	}
	if state := ParseScreen(question + "\n›"); state.Interactive {
		t.Fatalf("ordinary numbered transcript became picker: %+v", state)
	}
}
