package telegramrich

import (
	"strings"

	"bria/internal/markdownliteral"
)

func normalizeRichBlockquotes(text string) string {
	lines := strings.Split(text, "\n")
	literal := markdownliteral.Lines(lines)
	result := make([]string, 0, len(lines)+2)
	quoted := false
	for index, line := range lines {
		content, ok := richBlockquoteLine(line)
		if literal[index] {
			ok = false
		}
		if ok && !quoted {
			content = "<blockquote>" + content
			quoted = true
		}
		if !ok && quoted {
			result[len(result)-1] += "</blockquote>"
			quoted = false
		}
		if ok {
			result = append(result, content)
		} else {
			result = append(result, line)
		}
	}
	if quoted {
		result[len(result)-1] += "</blockquote>"
	}
	return strings.Join(result, "\n")
}

func richBlockquoteLine(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	for _, marker := range []string{"&gt;", ">"} {
		if !strings.HasPrefix(trimmed, marker) {
			continue
		}
		content := strings.TrimPrefix(trimmed, marker)
		if content == "" {
			return "", true
		}
		if content[0] != ' ' && content[0] != '\t' {
			return "", false
		}
		return strings.TrimLeft(content, " \t"), true
	}
	return "", false
}
