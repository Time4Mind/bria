package telegramnativeview_test

import (
	"os"
	"strings"
	"testing"

	"bria/internal/telegramnativeview"
)

func TestCommandApprovalPreservesStructuredLiteralContent(t *testing.T) {
	body, err := os.ReadFile("../nativeapproval/testdata/codex-command-approval-tracker-description.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := telegramnativeview.CommandApproval(string(body))
	if !ok || !strings.Contains(got, "**Command**\n\n```shell\n") ||
		!strings.Contains(got, `d=re.sub(r"[A-Za-z0-9._%+-]+@`) ||
		!strings.Contains(got, "**Reason**\n\n```text\n") {
		t.Fatalf("approval is not structured literal Rich Markdown:\n%s", got)
	}
	if strings.Count(got, "```text") != 3 || strings.Count(got, "```shell") != 1 {
		t.Fatalf("unexpected literal sections:\n%s", got)
	}
}

func TestCommandApprovalFormatsIncompletePreviewAndRejectsGenericPicker(t *testing.T) {
	body, err := os.ReadFile("../nativeapproval/testdata/codex-command-approval-collapsed.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := telegramnativeview.CommandApproval(string(body))
	if !ok || !strings.Contains(got, "[… 12 lines] ctrl + a view all") {
		t.Fatalf("collapsed approval = %q, %t", got, ok)
	}
	picker := "Select model and effort\n› 1. gpt-5.6_luna\n  2. codex-mini\nPress enter to confirm or esc to go back"
	if got, ok := telegramnativeview.CommandApproval(picker); ok || got != "" {
		t.Fatalf("generic picker formatted as approval: %q", got)
	}
}

func TestCommandApprovalFormatsPublicApprovalCorpus(t *testing.T) {
	for _, name := range []string{"codex-command-approval.txt", "codex-command-approval-wiki.txt", "codex-command-approval-tracker-description.txt"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile("../nativeapproval/testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := telegramnativeview.CommandApproval(string(body))
			if !ok || len(got) > 3600 || !strings.HasSuffix(got, "_Press enter to confirm or esc to cancel_") {
				t.Fatalf("formatted approval length=%d ok=%t", len(got), ok)
			}
		})
	}
}

func TestCommandApprovalBoundsLongPersistentCommandAndClosesFences(t *testing.T) {
	command := strings.Repeat("`", 3000)
	screen := "Would you like to run the following command?\n\nEnvironment: local\n\nReason: inspect output\n\n$ " + command + "\n\n› 1. Yes, proceed (y)\n  2. Yes, and don't ask again for commands that start with `" + command + "` (p)\n  3. No, and tell Codex what to do differently (esc)\n\nPress enter to confirm or esc to cancel"
	got, ok := telegramnativeview.CommandApproval(screen)
	if !ok || len([]rune(got)) > 3600 {
		t.Fatalf("bounded approval length=%d ok=%t", len([]rune(got)), ok)
	}
	if strings.Contains(got, "don't ask again for commands that start with ````") || !strings.Contains(got, "[… truncated]") {
		t.Fatalf("persistent option leaked command or command was not bounded:\n%s", got)
	}
	if strings.Count(got, "```shell") != 1 || strings.Count(got, "\n```") < 3 {
		t.Fatalf("literal fences are not closed:\n%s", got)
	}
}
