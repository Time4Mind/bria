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
	// Reserve room for a closing/reopening fence on every fragment.
	parts := splitText(text, maxBytes-80)
	fence := ""
	for i, part := range parts {
		prefix := ""
		if fence != "" {
			prefix = fence + "\n"
		}
		for _, line := range strings.Split(part, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				if fence == "" {
					fence = strings.TrimSpace(line)
					if len(fence) > 64 {
						fence = "```"
					}
				} else {
					fence = ""
				}
			}
		}
		suffix := ""
		if fence != "" {
			suffix = "\n```"
		}
		parts[i] = prefix + part + suffix
	}
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
