package telegramrich

import (
	"html"
	"strings"
)

// normalizeRichCodeFences converts ordinary Markdown fences into the HTML form
// rendered natively by Telegram Rich Markdown, including inside details blocks.
func normalizeRichCodeFences(text string) string {
	lines := strings.Split(text, "\n")
	normalized := make([]string, 0, len(lines))
	for index := 0; index < len(lines); {
		marker, language, fenced := richFenceOpener(lines[index])
		if !fenced {
			normalized = append(normalized, lines[index])
			index++
			continue
		}
		closing := index + 1
		for closing < len(lines) && !richFenceClosing(lines[closing], marker) {
			closing++
		}
		if closing == len(lines) {
			normalized = append(normalized, lines[index:]...)
			break
		}
		if marker[0] != '`' || !validRichCodeLanguage(language) || closing == index+1 {
			normalized = append(normalized, lines[index:closing+1]...)
			index = closing + 1
			continue
		}
		body := strings.Join(lines[index+1:closing], "\n")
		if language == "" && !strings.Contains(body, "\n") && !strings.Contains(body, "`") {
			normalized = append(normalized, "`"+body+"`")
		} else {
			attribute := ""
			if language != "" {
				attribute = ` class="language-` + language + `"`
			}
			normalized = append(normalized, "<pre><code"+attribute+">"+html.EscapeString(body)+"</code></pre>")
		}
		index = closing + 1
	}
	return strings.Join(normalized, "\n")
}

func richFenceOpener(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return "", "", false
	}
	count := 0
	for count < len(trimmed) && trimmed[count] == trimmed[0] {
		count++
	}
	if count < 3 {
		return "", "", false
	}
	return trimmed[:count], strings.ToLower(strings.TrimSpace(trimmed[count:])), true
}

func richFenceClosing(line, marker string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < len(marker) || strings.Trim(trimmed, marker[:1]) != "" {
		return false
	}
	return len(trimmed) >= len(marker)
}

func validRichCodeLanguage(language string) bool {
	if language == "" {
		return true
	}
	for _, r := range language {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '+' && r != '-' {
			return false
		}
	}
	return true
}
