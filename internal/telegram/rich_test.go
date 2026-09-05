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
