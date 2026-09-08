package telegram_test

import (
	"strings"
	"testing"

	"bria/internal/telegram"
)

func TestNormalizeRichMarkdownCompactsTable(t *testing.T) {
	got := telegram.NormalizeRichMarkdown("Статус\n\n\u00a0\n\n| Сервер | Бэк |\n|---|---|\n| local | codex |")
	if !strings.Contains(got, "| <sub>Сервер</sub> | <sub>Бэк</sub> |") || !strings.Contains(got, "| <sub>local</sub> | <sub>codex</sub> |") {
		t.Fatalf("normalized table = %q", got)
	}
}

func TestNormalizeRichMarkdownPreservesEscapesAndNonTables(t *testing.T) {
	cases := []struct{ input, want string }{
		{"Before\n| A | B |\n|:---|---:|\n| x\\|y |  |\nAfter", "Before\n\n| <sub>A</sub> | <sub>B</sub> |\n|:---|---:|\n| <sub>x\\|y</sub> |  |\nAfter"},
		{"| ordinary | text |\n| not | separator |", "| ordinary | text |\n| not | separator |"},
		{"plain\ntext", "plain\ntext"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := telegram.NormalizeRichMarkdown(tc.input); got != tc.want {
			t.Errorf("normalize %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}
