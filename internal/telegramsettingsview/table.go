package telegramsettingsview

import (
	"html"
	"strings"
)

type Field struct{ Name, Value string }

func table(title, firstColumn, secondColumn string, fields ...Field) Surface {
	return AppendFields(Surface{Text: tableCell(title) + "\n\n\u00a0\n\n| " + firstColumn + " | " + secondColumn + " |\n|---|---|", RichMarkdown: true}, fields...)
}

// AppendFields keeps node-specific defaults in the same escaped settings table.
func AppendFields(surface Surface, fields ...Field) Surface {
	for _, field := range fields {
		surface.Text += "\n| " + tableCell(field.Name) + " | " + tableCell(field.Value) + " |"
	}
	return surface
}

func tableCell(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	return strings.NewReplacer("\\", "\\\\", "|", "\\|", "`", "\\`", "*", "\\*", "_", "\\_", "~", "\\~", "[", "\\[", "]", "\\]").Replace(html.EscapeString(value))
}
