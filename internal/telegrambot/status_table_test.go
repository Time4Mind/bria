package telegrambot

import (
	"strings"
	"testing"
)

func TestRichTextNormalizesSixColumnStatusTable(t *testing.T) {
	rich, err := buildRichTextMessage("Status\n\n| Server | Back | Used | Age | Today | Reset |\n|---|---|---|---:|---:|---|\n| node | codex | week 50% | 2 | -4.0% | 20.08 12:30 |")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"| <sub>Server</sub> | <sub>Back</sub> | <sub>Used</sub> | <sub>Age</sub> | <sub>Today</sub> | <sub>Reset</sub> |",
		"| <sub>node</sub> | <sub>codex</sub> | <sub>week 50%</sub> | <sub>2</sub> | <sub>-4.0%</sub> | <sub>20.08 12:30</sub> |",
	} {
		if !strings.Contains(rich.Markdown, value) {
			t.Fatalf("rich table=%q does not contain %q", rich.Markdown, value)
		}
	}
}

func TestRichTextTableKeepsEscapedPipeInsideCell(t *testing.T) {
	rich, err := buildRichTextMessage(
		"Archive\n\n| Name | Description |\n|---|---|\n| a \\| b | first<br>second |",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "| <sub>a \\| b</sub> | <sub>first<br>second</sub> |"
	if !strings.Contains(rich.Markdown, want) {
		t.Fatalf("rich table=%q does not contain %q", rich.Markdown, want)
	}
}
