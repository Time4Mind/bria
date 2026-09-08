// Package markdownliteral identifies literal code boundaries in Markdown.
package markdownliteral

import (
	"regexp"
	"strings"
)

var literalToken = regexp.MustCompile("(?i)</?(?:code|pre)(?:[\\t ]+[^>]*|)>|`+")

// Lines marks lines that start inside code, and Markdown fence lines.
func Lines(lines []string) []bool {
	blocked := make([]bool, len(lines))
	fence, ticks := "", ""
	var tags []string
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		blocked[i] = fence != "" || ticks != "" || len(tags) > 0
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.TrimSpace(strings.TrimLeft(trimmed, fence[:1])) == "" {
				fence = ""
			}
			continue
		}
		if ticks == "" && len(tags) == 0 && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			end := strings.IndexFunc(trimmed, func(r rune) bool { return r != rune(trimmed[0]) })
			if end < 0 {
				end = len(trimmed)
			}
			if trimmed[0] == '~' || !strings.Contains(trimmed[end:], "`") {
				fence, blocked[i] = trimmed[:end], true
				continue
			}
		}
		for _, span := range literalToken.FindAllStringIndex(line, -1) {
			token := strings.ToLower(line[span[0]:span[1]])
			if token[0] == '`' && len(tags) == 0 {
				backslashes := 0
				for j := span[0] - 1; j >= 0 && line[j] == '\\'; j-- {
					backslashes++
				}
				if ticks == "" && backslashes%2 == 0 {
					ticks = token
				} else if ticks == token {
					ticks = ""
				}
			} else if token[0] == '<' && ticks == "" {
				name := strings.Fields(strings.Trim(token, "</>"))[0]
				if strings.HasPrefix(token, "</") {
					if len(tags) > 0 && tags[len(tags)-1] == name {
						tags = tags[:len(tags)-1]
					}
				} else {
					tags = append(tags, name)
				}
			}
		}
	}
	return blocked
}
