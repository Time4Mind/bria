package interactive_test

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/interactive"
)

func TestDetectsProviderPromptsAndClippedFallbacks(t *testing.T) {
	tests := []struct {
		name string
		pane string
		kind string
	}{
		{"plan", "Would you like to proceed?\n  1. Yes\nEsc to cancel\n", "plan_approval"},
		{"question", "☐ Pick one\n❯ 1. A\nEnter to select\n", "question"},
		{"permission", "Do you want to make this edit?\n  1. Yes\nEsc to cancel\n", "permission"},
		{"codex", "Would you like to run this command?\n› 1. Yes\nPress enter to confirm or esc to cancel\n", "codex_approval"},
		{"codex clipped", "3. No, do something else (esc)\nPress enter to confirm or esc to cancel\n", "codex_approval"},
		{"permission numbered", "› 1. Yes\n  2. No\n  3. Always\n", "permission"},
		{"question clipped", "❯ 2. Choice\n  3. Other\nEnter to select\n", "question"},
		{"multiselect submit", "1. [x] A\n❯ Submit\nEnter to select\n", "question"},
		{"bash", "Bash command\n  rm example\nEsc to cancel\n", "bash_approval"},
		{"restore", "Restore the code to a previous state?\n  details\nEnter to continue\n", "restore_checkpoint"},
		{"resume", "This session is 3 days old\n  1. Summary\nEnter to confirm\n", "resume_summary"},
		{"settings", "Select model\n  1. fast\nEsc to exit\n", "settings"},
		{"hooks trust", "Hooks need review\n1. Review hooks\n2. Trust all and continue\nPress enter to confirm or esc to go back\n", "hooks_trust"},
		{"codex update", "✨\u200aUpdate available! 0.97.0 -> 0.104.0\n\nRelease notes: https://github.com/openai/codex/releases/latest\n\n› 1. Update now\n  2. Skip\n  3. Skip until next version\n\nPress enter to continue\n", "codex_update"},
		{"codex update clipped", "› 1. Update now\n  2. Skip\n  3. Skip until next version\nPress enter to continue\n", "codex_update"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt, ok := interactive.Detect([]byte(test.pane))
			if !ok || prompt.Kind != test.kind || len(prompt.Hash) != 32 {
				t.Fatalf("prompt=%#v ok=%v", prompt, ok)
			}
		})
	}
}

func TestDetectionStripsANSIAndBoundsContent(t *testing.T) {
	pane := "\x1b[31mWould you like to proceed?\x1b[0m\n" +
		strings.Repeat("detail\n", 800) + "Esc to cancel\n"
	prompt, ok := interactive.Detect([]byte(pane))
	if !ok || len([]rune(prompt.Content)) > 3602 || strings.Contains(prompt.Content, "\x1b") {
		t.Fatalf("unexpected prompt length=%d ok=%v", len([]rune(prompt.Content)), ok)
	}
}

func TestOrdinaryNumberedOutputDoesNotMatch(t *testing.T) {
	if prompt, ok := interactive.Detect([]byte("1. build\n2. test\n3. ship\n")); ok {
		t.Fatalf("false positive: %#v", prompt)
	}
}
