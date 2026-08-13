package application

import (
	"strings"
	"unicode/utf8"
)

func packCardEventPages(blocks []cardEventBlock, limit int) []string {
	if len(blocks) == 0 {
		return []string{""}
	}
	pages := make([]string, 0, 1)
	current := ""
	for _, block := range blocks {
		if block.pageBreak && current != "" {
			pages = append(pages, current)
			current = ""
		}
		candidate := block.text
		if current != "" {
			candidate = current + cardEventJoiner + block.text
		}
		if current != "" && utf8.RuneCountInString(candidate) > limit {
			pages = append(pages, current)
			current = block.text
		} else {
			current = candidate
		}
	}
	if current != "" {
		pages = append(pages, current)
	}
	return pages
}

func splitCardText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	remaining := []rune(text)
	chunks := make([]string, 0, 1)
	for len(remaining) > limit {
		cut := cardTextRuneCut(remaining, limit)
		chunk := strings.TrimSpace(string(remaining[:cut]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		remaining = trimLeadingCardSpace(remaining[cut:])
	}
	if len(remaining) > 0 {
		chunks = append(chunks, strings.TrimSpace(string(remaining)))
	}
	return chunks
}

func cardTextRuneCut(text []rune, limit int) int {
	prefix := text[:limit]
	for _, boundary := range [][]rune{{'\n', '\n'}, {'\n'}, {'.', ' '}, {'!', ' '}, {'?', ' '}, {' '}} {
		if index := lastRuneSequence(prefix, boundary); index > 0 {
			return index + len(boundary)
		}
	}
	return limit
}

func lastRuneSequence(text, sequence []rune) int {
	for index := len(text) - len(sequence); index >= 0; index-- {
		match := true
		for offset := range sequence {
			if text[index+offset] != sequence[offset] {
				match = false
				break
			}
		}
		if match {
			return index
		}
	}
	return -1
}

func trimLeadingCardSpace(text []rune) []rune {
	for len(text) > 0 {
		switch text[0] {
		case ' ', '\n', '\r', '\t':
			text = text[1:]
		default:
			return text
		}
	}
	return text
}
