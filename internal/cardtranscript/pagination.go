package cardtranscript

import "strings"

// Split keeps each page independently renderable, including a long code fence
// or spoiler. Wrapper bytes are part of the page budget.
func Split(text string, maxBytes int) []string {
	if len(text) <= maxBytes {
		return []string{text}
	}
	if strings.HasPrefix(text, "<details><summary>") {
		if end := strings.Index(text, "</summary>\n\n"); end >= 0 {
			end += len("</summary>\n\n")
			prefix, suffix := text[:end], "\n\n</details>"
			body := strings.TrimSuffix(text[end:], suffix)
			parts := splitText(body, maxBytes-len(prefix)-len(suffix))
			for i := range parts {
				parts[i] = prefix + parts[i] + suffix
			}
			return parts
		}
	}
	if parts := splitTables(text, maxBytes); parts != nil {
		return parts
	}
	return splitMarkdown(text, maxBytes)
}

func splitMarkdown(text string, maxBytes int) []string {
	lines := strings.SplitAfter(text, "\n")
	var parts []string
	start := 0
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "```") && !strings.HasPrefix(line, "~~~") {
			continue
		}
		marker := strings.TrimSuffix(line, strings.TrimLeft(line, line[:1]))
		end := i + 1
		for end < len(lines) {
			closing := strings.TrimSpace(lines[end])
			if strings.HasPrefix(closing, marker) && strings.TrimSpace(strings.TrimLeft(closing, marker[:1])) == "" {
				break
			}
			end++
		}
		opening, closing, body := lines[i], "\n"+marker, strings.Join(lines[i+1:end], "")
		if end < len(lines) {
			closing = lines[end]
			end++
		}
		budget := maxBytes - len(opening) - len(closing) - 1
		if budget < 4 || body == "" {
			// Impossible wrappers retain source bytes as bounded text.
			opening, closing, body, budget = "", "", strings.Join(lines[i:end], ""), maxBytes
		}
		parts = append(parts, splitText(strings.Join(lines[start:i], ""), maxBytes)...)
		chunks := splitText(body, budget)
		for n, chunk := range chunks {
			suffix := closing
			if n < len(chunks)-1 && opening != "" {
				suffix = "\n" + marker
			}
			parts = append(parts, opening+chunk+suffix)
		}
		i, start = end-1, end
	}
	parts = append(parts, splitText(strings.Join(lines[start:], ""), maxBytes)...)
	return parts
}

func splitText(text string, budget int) []string {
	parts := []string{}
	for len(text) > budget {
		end := budget
		for end > 0 && text[end]&0xc0 == 0x80 {
			end--
		}
		// Avoid slicing an HTML entity or a Markdown fence delimiter.
		if amp := strings.LastIndexByte(text[:end], '&'); amp >= 0 && !strings.Contains(text[amp:end], ";") && end-amp < 16 {
			end = amp
		}
		for end > 0 && text[end-1] == '`' {
			end--
		}
		if line := strings.LastIndexByte(text[:end], '\n'); line > budget/2 {
			end = line + 1
		}
		if end == 0 {
			end = budget
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	if text != "" {
		parts = append(parts, text)
	}
	return parts
}
