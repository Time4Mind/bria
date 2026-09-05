package telegram

import "strings"

// NormalizeRichMarkdown applies the compact table layout expected by Telegram.
func NormalizeRichMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	for index := 0; index < len(lines); {
		if !strings.HasPrefix(strings.TrimSpace(lines[index]), "|") {
			index++
			continue
		}
		end := index
		for end < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[end]), "|") {
			end++
		}
		if end-index >= 2 && isRichTableSeparator(lines[index+1]) {
			if index > 0 && strings.TrimSpace(lines[index-1]) != "" {
				lines = append(lines[:index], append([]string{""}, lines[index:]...)...)
				index++
				end++
			}
			for row := index; row < end; row++ {
				if !isRichTableSeparator(lines[row]) {
					lines[row] = subWrapRichTableRow(lines[row])
				}
			}
		}
		index = end
	}
	return strings.Join(lines, "\n")
}

func isRichTableSeparator(line string) bool {
	cells := splitRichTableCells(line)
	if len(cells) == 0 {
		return false
	}
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
		if value != "" {
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
