package application

import (
	"strings"
	"unicode/utf8"
)

func packCardEventPages(blocks []cardEventBlock, runeLimit, byteLimit int) []string {
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
		if current != "" && cardTextTooLong(candidate, runeLimit, byteLimit) {
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

func splitCardText(text string, runeLimit, byteLimit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	remaining := []rune(text)
	chunks := make([]string, 0, 1)
	for cardRunesTooLong(remaining, runeLimit, byteLimit) {
		prefixLimit := cardPrefixLimit(remaining, runeLimit, byteLimit)
		cut := cardTextRuneCut(remaining, prefixLimit)
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

func cardTextTooLong(text string, runeLimit, byteLimit int) bool {
	return utf8.RuneCountInString(text) > runeLimit || len(text) > byteLimit
}

func cardRunesTooLong(text []rune, runeLimit, byteLimit int) bool {
	return len(text) > runeLimit || len(string(text)) > byteLimit
}

func cardPrefixLimit(text []rune, runeLimit, byteLimit int) int {
	limit := min(len(text), runeLimit)
	limit = min(limit, runePrefixWithinBytes(text[:limit], byteLimit))
	return max(1, limit)
}

func runePrefixWithinBytes(text []rune, byteLimit int) int {
	used := 0
	for index, value := range text {
		size := utf8.RuneLen(value)
		if size < 1 || used+size > byteLimit {
			return index
		}
		used += size
	}
	return len(text)
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
