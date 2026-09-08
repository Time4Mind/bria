// Package telegramrich normalizes rich Markdown for Telegram presentation.
package telegramrich

import (
	"strings"

	"bria/internal/markdownliteral"
)

// NormalizeRichMarkdown applies the compact table layout expected by Telegram.
func NormalizeRichMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	rows := tableRows(lines)
	var result []string
	for i, line := range lines {
		if rows[i] {
			if i == 0 {
				result = append(result, "", "")
			} else if !rows[i-1] && strings.TrimSpace(lines[i-1]) != "" {
				result = append(result, "")
			}
			if !isRichTableSeparator(line) {
				line = subWrapRichTableRow(line)
			}
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func tableRows(lines []string) []bool {
	blocked, rows := markdownliteral.Lines(lines), make([]bool, len(lines))
	for i := 0; i+1 < len(lines); i++ {
		if blocked[i] || blocked[i+1] || !strings.HasPrefix(strings.TrimSpace(lines[i]), "|") ||
			!isRichTableSeparator(lines[i+1]) || len(splitRichTableCells(lines[i])) != len(splitRichTableCells(lines[i+1])) {
			continue
		}
		rows[i], rows[i+1] = true, true
		for i += 2; i < len(lines) && !blocked[i] && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
			rows[i] = true
		}
		i--
	}
	return rows
}

func isRichTableSeparator(line string) bool {
	cells := splitRichTableCells(line)
	for _, cell := range cells {
		value := strings.Trim(strings.TrimSpace(cell), ":")
		if len(value) < 3 || strings.Trim(value, "-") != "" {
			return false
		}
	}
	return true
}

func subWrapRichTableRow(line string) string {
	cells := splitRichTableCells(line)
	for index, cell := range cells {
		value := strings.TrimSpace(cell)
		if value != "" && !(strings.HasPrefix(value, "<sub>") && strings.HasSuffix(value, "</sub>")) {
			cells[index] = " <sub>" + value + "</sub> "
		}
	}
	return "|" + strings.Join(cells, "|") + "|"
}

func splitRichTableCells(line string) []string {
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "|"), "|")
	cells := make([]string, 0, 6)
	start := 0
	escaped := false
	for index := 0; index < len(body); index++ {
		switch body[index] {
		case '\\':
			escaped = !escaped
			continue
		case '|':
			if !escaped {
				cells = append(cells, body[start:index])
				start = index + 1
			}
		}
		escaped = false
	}
	return append(cells, body[start:])
}
