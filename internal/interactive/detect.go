// Package interactive detects bounded interactive CLI regions in captured
// terminal panes. Detection is host-local; callers may replicate only Prompt
// metadata and must keep Content on the origin node.
package interactive

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const maxContentRunes = 3600

type Prompt struct {
	Kind    string
	Hash    string
	Content string
}

func Detect(pane []byte) (Prompt, bool) {
	clean := normalizeTerminal(string(pane))
	lines := strings.Split(clean, "\n")
	for _, candidate := range patterns {
		content, ok := candidate.extract(lines)
		if !ok {
			continue
		}
		content = shortenSeparators(strings.TrimSpace(content))
		content = tailRunes(content, maxContentRunes)
		digest := sha256.Sum256([]byte(candidate.kind + "\x00" + content))
		return Prompt{
			Kind: candidate.kind, Hash: hex.EncodeToString(digest[:16]), Content: content,
		}, true
	}
	return Prompt{}, false
}

func (p Prompt) VerticalOnly() bool {
	return p.Kind == "restore_checkpoint" || p.Kind == "codex_update"
}

func (p pattern) extract(lines []string) (string, bool) {
	for _, exclude := range p.exclude {
		for _, line := range lines {
			if exclude.MatchString(line) {
				return "", false
			}
		}
	}
	top, bottom := -1, -1
	for index, line := range lines {
		if top < 0 {
			if matchesAny(p.top, line) {
				top = index
			}
			continue
		}
		if len(p.bottom) > 0 && matchesAny(p.bottom, line) {
			bottom = index
			break
		}
	}
	if top < 0 {
		return "", false
	}
	if len(p.bottom) == 0 {
		for index := len(lines) - 1; index > top; index-- {
			if strings.TrimSpace(lines[index]) != "" {
				bottom = index
				break
			}
		}
	}
	if bottom < 0 || bottom-top < p.minGap {
		return "", false
	}
	return strings.Join(lines[top:bottom+1], "\n"), true
}

func normalizeTerminal(value string) string {
	var out strings.Builder
	for index := 0; index < len(value); {
		if value[index] != 0x1b {
			if value[index] != '\r' {
				out.WriteByte(value[index])
			}
			index++
			continue
		}
		index++
		if index >= len(value) {
			break
		}
		switch value[index] {
		case '[':
			index++
			for index < len(value) {
				char := value[index]
				index++
				if char >= 0x40 && char <= 0x7e {
					break
				}
			}
		case ']':
			index++
			for index < len(value) {
				if value[index] == '\a' {
					index++
					break
				}
				if value[index] == 0x1b && index+1 < len(value) && value[index+1] == '\\' {
					index += 2
					break
				}
				index++
			}
		default:
			index++
		}
	}
	return out.String()
}

func matchesAny(expressions expressions, value string) bool {
	for _, expression := range expressions {
		if expression.MatchString(value) {
			return true
		}
	}
	return false
}

func shortenSeparators(value string) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len([]rune(trimmed)) >= 5 && strings.Trim(trimmed, "─") == "" {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[index] = indent + "─────"
		}
	}
	return strings.Join(lines, "\n")
}

func tailRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return "…\n" + string(runes[len(runes)-limit:])
}
