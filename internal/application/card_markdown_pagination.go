package application

import "strings"

type cardMarkdownSegment struct {
	text  string
	table *cardMarkdownTable
}

type cardMarkdownTable struct {
	header    string
	separator string
	rows      []string
}

// splitCardText keeps GFM tables structurally valid on every Telegram page.
// A continuation page repeats the table header and separator, while data rows
// remain atomic. This matters because a page beginning with body rows is not a
// Markdown table and Telegram otherwise renders it as broken plain text.
func splitCardText(text string, runeLimit, byteLimit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	segments := splitCardMarkdownSegments(text)
	pages := make([]string, 0, 1)
	current := ""
	flush := func() {
		if strings.TrimSpace(current) != "" {
			pages = append(pages, strings.TrimSpace(current))
		}
		current = ""
	}
	appendChunk := func(chunk string) {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			return
		}
		if current == "" {
			current = chunk
			return
		}
		candidate := current + "\n\n" + chunk
		if cardTextTooLong(candidate, runeLimit, byteLimit) {
			flush()
			current = chunk
			return
		}
		current = candidate
	}

	for _, segment := range segments {
		if segment.table != nil {
			tableChunks := splitCardMarkdownTable(*segment.table, runeLimit, byteLimit)
			for index, chunk := range tableChunks {
				appendChunk(chunk)
				// Every chunk except the final one is a complete continuation page.
				if index < len(tableChunks)-1 {
					flush()
				}
			}
			continue
		}
		chunks := splitCardPlainText(segment.text, runeLimit, byteLimit)
		for index, chunk := range chunks {
			appendChunk(chunk)
			if index < len(chunks)-1 {
				flush()
			}
		}
	}
	flush()
	return pages
}

func splitCardMarkdownSegments(text string) []cardMarkdownSegment {
	lines := strings.Split(text, "\n")
	segments := make([]cardMarkdownSegment, 0, 3)
	plainStart := 0
	inFence := false
	for index := 0; index < len(lines); {
		trimmed := strings.TrimSpace(lines[index])
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			index++
			continue
		}
		if inFence || index+1 >= len(lines) || !isCardMarkdownRow(lines[index]) ||
			!isCardMarkdownSeparator(lines[index+1]) {
			index++
			continue
		}
		if plain := strings.TrimSpace(strings.Join(lines[plainStart:index], "\n")); plain != "" {
			segments = append(segments, cardMarkdownSegment{text: plain})
		}
		end := index + 2
		for end < len(lines) && isCardMarkdownRow(lines[end]) {
			end++
		}
		segments = append(segments, cardMarkdownSegment{table: &cardMarkdownTable{
			header:    strings.TrimSpace(lines[index]),
			separator: strings.TrimSpace(lines[index+1]),
			rows:      trimmedCardMarkdownRows(lines[index+2 : end]),
		}})
		index = end
		plainStart = end
	}
	if plain := strings.TrimSpace(strings.Join(lines[plainStart:], "\n")); plain != "" {
		segments = append(segments, cardMarkdownSegment{text: plain})
	}
	return segments
}

func trimmedCardMarkdownRows(lines []string) []string {
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, strings.TrimSpace(line))
	}
	return rows
}

func splitCardMarkdownTable(table cardMarkdownTable, runeLimit, byteLimit int) []string {
	prefix := table.header + "\n" + table.separator
	if len(table.rows) == 0 {
		return []string{prefix}
	}
	chunks := make([]string, 0, 1)
	current := prefix
	for _, row := range table.rows {
		candidate := current + "\n" + row
		if !cardTextTooLong(candidate, runeLimit, byteLimit) {
			current = candidate
			continue
		}
		if current != prefix {
			chunks = append(chunks, current)
			current = prefix
		}
		candidate = current + "\n" + row
		if cardTextTooLong(candidate, runeLimit, byteLimit) {
			// An individual row cannot form a valid bounded table. Preserve all
			// content using the ordinary bounded paginator instead of truncating it.
			if current != prefix {
				chunks = append(chunks, current)
			}
			chunks = append(chunks, splitCardPlainText(row, runeLimit, byteLimit)...)
			current = prefix
			continue
		}
		current = candidate
	}
	if current != prefix || len(chunks) == 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func isCardMarkdownRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") &&
		strings.Count(trimmed, "|") >= 2
}

func isCardMarkdownSeparator(line string) bool {
	if !isCardMarkdownRow(line) {
		return false
	}
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for _, cell := range cells {
		value := strings.Trim(strings.TrimSpace(cell), ":")
		if len(value) < 3 || strings.Trim(value, "-") != "" {
			return false
		}
	}
	return len(cells) > 0
}
