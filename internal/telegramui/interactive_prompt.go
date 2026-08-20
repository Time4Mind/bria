package telegramui

import (
	"regexp"
	"strings"
	"unicode"
)

var interactiveNumberedOption = regexp.MustCompile(`^\s*[>›❯▶↑↓]?\s*\d+\.\s`)

const interactiveBlockSeparator = "\n\n─────\n\n"

// FormatInteractivePrompt mirrors CCBot's proven mobile layout: numbered
// choices become distinct blocks, wrapped descriptions stay with their choice,
// and terminal box borders cannot collapse the prompt into visual noise.
func FormatInteractivePrompt(raw string) string {
	raw = strings.ReplaceAll(raw, "\r", "")
	if containsBoxFrame(raw) {
		return separateInteractiveOptions(sanitizeInteractiveBox(raw))
	}
	paragraphs := interactiveParagraphs(raw)
	if len(paragraphs) == 0 {
		return ""
	}
	renderedParagraphs := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		blocks := make([]interactiveBlock, 0, len(paragraph))
		var current *interactiveBlock
		for _, line := range paragraph {
			if interactiveNumberedOption.MatchString(line) {
				blocks = append(blocks, interactiveBlock{option: true})
				current = &blocks[len(blocks)-1]
			} else if current == nil {
				blocks = append(blocks, interactiveBlock{})
				current = &blocks[len(blocks)-1]
			}
			current.lines = append(current.lines, line)
		}
		var rendered strings.Builder
		for index, block := range blocks {
			if index > 0 {
				separator := "\n\n"
				if blocks[index-1].option && block.option {
					separator = interactiveBlockSeparator
				}
				rendered.WriteString(separator)
			}
			rendered.WriteString(strings.Join(block.lines, "\n"))
		}
		renderedParagraphs = append(renderedParagraphs, rendered.String())
	}
	return strings.Join(renderedParagraphs, "\n\n")
}

type interactiveBlock struct {
	lines  []string
	option bool
}

func interactiveParagraphs(raw string) [][]string {
	paragraphs := make([][]string, 0)
	current := make([]string, 0)
	flush := func() {
		if len(current) > 0 {
			paragraphs = append(paragraphs, current)
			current = nil
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || interactiveRuleLine(trimmed) {
			flush()
			continue
		}
		current = append(current, strings.TrimRightFunc(line, unicode.IsSpace))
	}
	flush()
	return paragraphs
}

func separateInteractiveOptions(raw string) string {
	lines := make([]string, 0)
	seenOption := false
	for _, line := range strings.Split(raw, "\n") {
		if interactiveRuleLine(strings.TrimSpace(line)) {
			continue
		}
		if interactiveNumberedOption.MatchString(line) {
			if seenOption {
				lines = append(lines, "─────")
			}
			seenOption = true
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func interactiveRuleLine(value string) bool {
	if len([]rune(value)) < 3 {
		return false
	}
	for _, char := range value {
		if char != '─' && char != '-' && char != '=' && char != '_' {
			return false
		}
	}
	return true
}

func containsBoxFrame(value string) bool {
	for _, char := range value {
		if (char >= '│' && char <= '┃') || (char >= '┌' && char <= '╋') ||
			(char >= '═' && char <= '╬') {
			return true
		}
	}
	return false
}

func sanitizeInteractiveBox(raw string) string {
	lines := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		cleaned := strings.Map(func(char rune) rune {
			if char >= 0x2500 && char <= 0x259f {
				return -1
			}
			return char
		}, line)
		cleaned = strings.TrimRightFunc(cleaned, unicode.IsSpace)
		if strings.TrimSpace(cleaned) == "" {
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}
		lines = append(lines, cleaned)
	}
	return strings.Join(lines, "\n")
}
