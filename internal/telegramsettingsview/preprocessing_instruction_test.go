package telegramsettingsview

import (
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/telegramrich"
)

func TestPreprocessingInstructionEditorRendersCopyableLiteralPages(t *testing.T) {
	instruction := "Keep this line verbatim.\n```\nembedded fence\n```\n" + strings.Repeat("`", 1000) + "\n" + strings.Repeat("данные🙂\n", 600)

	var restored strings.Builder
	choice := 1
	for page := 1; ; page++ {
		surface := PreprocessingInstructionEditor(instruction, choice)
		wire := telegramrich.NormalizeRichMarkdown(surface.Text)
		if !surface.RichMarkdown || len([]byte(surface.Text)) > 4096 || len([]byte(wire)) > 4096 || !utf8.ValidString(surface.Text) {
			t.Fatalf("page %d shape: rich=%t bytes=%d wire=%d utf8=%t", page, surface.RichMarkdown, len([]byte(surface.Text)), len([]byte(wire)), utf8.ValidString(surface.Text))
		}

		const headingEnd = ":\n\n"
		start := strings.Index(surface.Text, headingEnd)
		end := strings.LastIndex(surface.Text, "\n\nОтправьте новую инструкцию")
		if start < 0 || end < start {
			t.Fatalf("page %d has no instruction block: %q", page, surface.Text)
		}
		block := surface.Text[start+len(headingEnd) : end]
		firstNewline := strings.IndexByte(block, '\n')
		lastNewline := strings.LastIndexByte(block, '\n')
		if firstNewline < 3 || lastNewline <= firstNewline {
			t.Fatalf("page %d is not a multi-line fenced block: %q", page, block)
		}
		fence := block[:firstNewline]
		if strings.Trim(fence, "`") != "" || block[lastNewline+1:] != fence {
			t.Fatalf("page %d has an unsafe or language-tagged fence: %q", page, block)
		}
		body := block[firstNewline+1 : lastNewline]
		if strings.Contains(body, "\n"+fence+"\n") {
			t.Fatalf("page %d body closes its own fence %q", page, fence)
		}
		restored.WriteString(body)

		nextChoice := 0
		for _, row := range surface.Rows {
			for _, button := range row {
				if button.Label == "Следующая" && button.Action == "settings_preprocessing_instruction" {
					nextChoice = button.Choice
				}
			}
		}
		if nextChoice == 0 {
			break
		}
		choice = nextChoice
	}
	if restored.String() != instruction {
		t.Fatalf("paged instruction changed: got %d bytes, want %d", restored.Len(), len([]byte(instruction)))
	}
}

func TestPreprocessingInstructionEditorUsesUnlabelledBlockForSingleLine(t *testing.T) {
	surface := PreprocessingInstructionEditor("keep one line verbatim", 1)
	if !surface.RichMarkdown || !strings.Contains(surface.Text, "\n\n```\nkeep one line verbatim\n```\n\n") {
		t.Fatalf("single-line instruction is not an unlabelled fenced block: %#v", surface)
	}
	if wire := telegramrich.NormalizeRichMarkdown(surface.Text); !strings.Contains(wire, "<pre><code>keep one line verbatim</code></pre>") {
		t.Fatalf("single-line instruction lost its copyable block on the wire: %q", wire)
	}
}

func TestPreprocessingInstructionEditorBoundsEscapedWirePages(t *testing.T) {
	instruction := strings.Repeat("<&\"'", 3200)
	for choice := 1; ; choice++ {
		surface := PreprocessingInstructionEditor(instruction, choice)
		if wire := telegramrich.NormalizeRichMarkdown(surface.Text); len([]byte(wire)) > 4096 {
			t.Fatalf("escaped page %d wire bytes = %d", choice, len([]byte(wire)))
		}
		hasNext := false
		for _, row := range surface.Rows {
			for _, button := range row {
				hasNext = hasNext || button.Label == "Следующая"
			}
		}
		if !hasNext {
			break
		}
	}
}
