package telegramformat

import (
	"reflect"
	"testing"

	"bria/internal/telegram"
)

func TestMarkdownBuildsTelegramEntitiesForProviderFinal(t *testing.T) {
	input := "**Проверка**\n\n- Рабочая папка: `/root`\n\n```text\n/root\n```"
	text, entities := Markdown(input)
	wantText := "Проверка\n\n- Рабочая папка: /root\n\n/root\n"
	if text != wantText {
		t.Fatalf("Markdown() text = %q, want %q", text, wantText)
	}
	want := []telegram.MessageEntity{
		{Type: "bold", Offset: 0, Length: utf16Length("Проверка")},
		{Type: "code", Offset: utf16Length("Проверка\n\n- Рабочая папка: "), Length: utf16Length("/root")},
		{Type: "pre", Offset: utf16Length("Проверка\n\n- Рабочая папка: /root\n\n"), Length: utf16Length("/root\n"), Language: "text"},
	}
	if !reflect.DeepEqual(entities, want) {
		t.Fatalf("Markdown() entities = %#v, want %#v", entities, want)
	}
}

func TestMarkdownLeavesUnmatchedOrUnsupportedMarkupLiteral(t *testing.T) {
	input := "literal **open and ```bad language\nbody``` plus `multi\nline`"
	text, entities := Markdown(input)
	if text != input || len(entities) != 0 {
		t.Fatalf("Markdown() = (%q, %#v), want literal input", text, entities)
	}
}
