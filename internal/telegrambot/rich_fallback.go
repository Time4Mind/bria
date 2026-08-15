package telegrambot

import (
	"html"
	"strings"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func richFallbackMarkdownV2(screen telegramui.Screen) string {
	rendered := renderRichFallbackMarkdownV2(screen.Text, true)
	if len(rendered) <= MaxMessageTextBytes {
		return rendered
	}
	// An adversarial page consisting almost entirely of Markdown control
	// characters can grow beyond 4096 bytes after escaping. In that exceptional
	// case keep every technical summary but omit the hidden bodies.
	plain := renderRichFallbackPlain(screen.Text, false)
	return escapeMarkdownV2Bounded(plain, MaxMessageTextBytes)
}

func renderRichFallbackMarkdownV2(text string, includeBodies bool) string {
	var result strings.Builder
	for len(text) > 0 {
		start := strings.Index(text, "<details><summary>")
		if start < 0 {
			result.WriteString(escapeMarkdownV2(html.UnescapeString(text)))
			break
		}
		result.WriteString(escapeMarkdownV2(html.UnescapeString(text[:start])))
		payload := text[start+len("<details><summary>"):]
		summaryEnd := strings.Index(payload, "</summary>")
		if summaryEnd < 0 {
			result.WriteString(escapeMarkdownV2(html.UnescapeString(text[start:])))
			break
		}
		summary := strings.TrimSpace(html.UnescapeString(payload[:summaryEnd]))
		bodyAndTail := payload[summaryEnd+len("</summary>"):]
		detailsEnd := strings.Index(bodyAndTail, "</details>")
		if detailsEnd < 0 {
			result.WriteString(escapeMarkdownV2(html.UnescapeString(text[start:])))
			break
		}
		body := strings.TrimSpace(html.UnescapeString(bodyAndTail[:detailsEnd]))
		if includeBodies && body != "" {
			result.WriteString(expandableMarkdownV2(summary, body))
		} else {
			result.WriteString(escapeMarkdownV2(summary))
		}
		text = bodyAndTail[detailsEnd+len("</details>"):]
	}
	return result.String()
}

func renderRichFallbackPlain(text string, includeBodies bool) string {
	markdown := renderRichFallbackMarkdownV2(text, includeBodies)
	return unescapeMarkdownV2(markdown)
}

func expandableMarkdownV2(summary, body string) string {
	lines := strings.Split(summary+"\n"+body, "\n")
	for index, line := range lines {
		lines[index] = ">" + escapeMarkdownV2(line)
	}
	return strings.Join(lines, "\n") + "||"
}

func escapeMarkdownV2(text string) string {
	const special = "_[]()~`>#+-=|{}.!*\\"
	var result strings.Builder
	result.Grow(len(text))
	for _, char := range text {
		if strings.ContainsRune(special, char) {
			result.WriteByte('\\')
		}
		result.WriteRune(char)
	}
	return result.String()
}

func unescapeMarkdownV2(text string) string {
	var result strings.Builder
	result.Grow(len(text))
	escaped := false
	for _, char := range text {
		if escaped {
			result.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		result.WriteRune(char)
	}
	if escaped {
		result.WriteByte('\\')
	}
	return result.String()
}

func escapeMarkdownV2Bounded(text string, limit int) string {
	const suffix = "…"
	var result strings.Builder
	for _, char := range text {
		piece := string(char)
		if strings.ContainsRune("_[]()~`>#+-=|{}.!*\\", char) {
			piece = "\\" + piece
		}
		if result.Len()+len(piece)+len(suffix) > limit {
			result.WriteString(suffix)
			return result.String()
		}
		result.WriteString(piece)
	}
	return result.String()
}
