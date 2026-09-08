// Package notificationplan prepares immutable Rich Markdown delivery payloads.
package notificationplan

import (
	"errors"
	"strings"
	"unicode/utf8"

	"bria/internal/cardtranscript"
	"bria/internal/telegramrich"
)

// Plan is the persisted delivery input identity and final wire payloads.
// Callers own versioning, identity verification and durable storage.
type Plan struct {
	Version   int      `json:"version"`
	InputHash string   `json:"input_hash"`
	Pages     []string `json:"pages"`
}

// Rich returns final payloads including prefix within a UTF-8 byte limit.
func Rich(prefix, text string, limit int) ([]string, error) {
	if !utf8.ValidString(prefix) || !utf8.ValidString(text) || limit < 1 || len(prefix) >= limit {
		return nil, errors.New("invalid notification page input or budget")
	}
	// Normalize in the actual prefix context: normalizing a standalone table
	// adds leading spacing that a notifier label does not need. Remove only
	// the unchanged prefix, retaining every authored body newline.
	normalized := telegramrich.NormalizeRichMarkdown(prefix + text)
	if strings.HasPrefix(normalized, prefix) {
		text = strings.TrimPrefix(normalized, prefix)
	} else {
		// Rich prefixes can themselves change; the final pass budgets those.
		text = telegramrich.NormalizeRichMarkdown(text)
	}
	for budget := limit - len(prefix); budget > 0; {
		parts, err := split(text, budget)
		if err != nil {
			return nil, err
		}
		overflow := 0
		for i, part := range parts {
			parts[i] = telegramrich.NormalizeRichMarkdown(prefix + part)
			if telegramrich.NormalizeRichMarkdown(parts[i]) != parts[i] {
				return nil, errors.New("notification normalization is not stable")
			}
			if excess := len(parts[i]) - limit; excess > overflow {
				overflow = excess
			}
		}
		if overflow == 0 {
			return parts, nil
		}
		// Splitting can add leading table spacing. Replan from the entire
		// normalized source with less room, never from a partial result.
		budget -= overflow
	}
	return nil, errors.New("notification prefix and normalization exceed budget")
}

func split(text string, budget int) ([]string, error) {
	// Split's details branch subtracts the wrapper without checking whether
	// a rune still fits; its byte splitter also requires room for a full rune.
	if budget < utf8.UTFMax {
		return bounded(text, budget)
	}
	if strings.HasPrefix(text, "<details><summary>") {
		if end := strings.Index(text, "</summary>\n\n"); end >= 0 &&
			end+len("</summary>\n\n")+len("\n\n</details>")+utf8.UTFMax > budget {
			return bounded(text, budget)
		}
	}
	parts := cardtranscript.Split(text, budget)
	for _, part := range parts {
		if len(part) > budget || !utf8.ValidString(part) {
			return bounded(text, budget)
		}
	}
	return parts, nil
}

func bounded(text string, budget int) ([]string, error) {
	var parts []string
	for len(text) > budget {
		end := budget
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == 0 {
			return nil, errors.New("notification budget cannot fit a rune")
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	return append(parts, text), nil
}
