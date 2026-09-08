package cardtranscript

import (
	"strings"

	"bria/internal/markdownliteral"
)

// splitTables repeats structural rows, retaining original row bytes. Literal
// fenced examples stay on the ordinary paginator; nil means no table was found.
func splitTables(text string, budget int) []string {
	lines := strings.SplitAfter(text, "\n")
	var parts []string
	start, blocked := 0, markdownliteral.Lines(lines)
	appendText := func(text string) {
		if text == "" {
			return
		}
		for _, part := range splitMarkdown(text, budget) {
			if n := len(parts); n > 0 && len(parts[n-1])+len(part) <= budget {
				parts[n-1] += part
			} else {
				parts = append(parts, part)
			}
		}
	}
	for i := 0; i+1 < len(lines); i++ {
		if blocked[i] || blocked[i+1] || !tableRow(lines[i]) || !tableSeparator(lines[i+1]) ||
			tableColumns(lines[i]) != tableColumns(lines[i+1]) {
			continue
		}
		appendText(strings.Join(lines[start:i], ""))
		end := i + 2
		for end < len(lines) && !blocked[end] && tableRow(lines[end]) {
			end++
		}
		prefix := lines[i] + lines[i+1]
		current := prefix
		for index, row := range lines[i+2 : end] {
			if len(current)+len(row) > budget && current != prefix {
				appendText(current)
				current = prefix
			}
			if len(prefix)+len(row) > budget {
				// A row too large for a bounded table retains its full text,
				// but cannot retain the table's visual shape on these pages.
				if index == 0 {
					appendText(prefix)
				}
				appendText(row)
				continue
			}
			current += row
		}
		if current != prefix || end == i+2 {
			appendText(current)
		}
		i, start = end-1, end
	}
	if parts != nil {
		appendText(strings.Join(lines[start:], ""))
	}
	return parts
}

func tableColumns(line string) int {
	line = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "|"), "|")
	count := 1
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' {
			i++
		} else if line[i] == '|' {
			count++
		}
	}
	return count
}

func tableRow(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|") && strings.Count(line, "|") >= 2
}

func tableSeparator(line string) bool {
	if !tableRow(line) {
		return false
	}
	for _, cell := range strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|") {
		value := strings.Trim(strings.TrimSpace(cell), ":")
		if len(value) < 3 || strings.Trim(value, "-") != "" {
			return false
		}
	}
	return true
}
