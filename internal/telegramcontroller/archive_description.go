package telegramcontroller

import (
	"context"
	"sort"
	"strings"

	"bria/internal/domain"
)

// Prompt identity is keyed metadata, never inferred from an emoji in arbitrary
// provider output. The storage port preserves this distinction across restart.
type archiveUserPromptReader interface {
	LoadCardUserPrompts(context.Context, domain.SessionID, int) ([]string, error)
}

const (
	archiveDescriptionPromptCount = 2
	archiveDescriptionPromptLimit = 60
	archiveDescriptionTotalLimit  = 240
)

func (c *Controller) archivePromptDescription(ctx context.Context, id domain.SessionID) []string {
	var prompts []string
	if reader, ok := c.uiState.(archiveUserPromptReader); ok {
		var err error
		prompts, err = reader.LoadCardUserPrompts(ctx, id, archiveDescriptionPromptCount)
		if err != nil {
			return nil
		}
	} else {
		c.mu.Lock()
		indexes := make([]int, 0, len(c.promptIndexes[id]))
		for _, index := range c.promptIndexes[id] {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			if len(prompts) == archiveDescriptionPromptCount {
				break
			}
			if index >= 0 && index < len(c.history[id]) {
				prompts = append(prompts, c.history[id][index])
			}
		}
		c.mu.Unlock()
	}
	lines := make([]string, 0, 3)
	remaining := archiveDescriptionTotalLimit
	for _, entry := range prompts[:min(archiveDescriptionPromptCount, len(prompts))] {
		entry = strings.TrimSpace(entry)
		entry = strings.TrimPrefix(entry, "❌ Ошибка препроцессинга\n")
		for _, marker := range []string{"🙋‍♂ ", "👨‍💻 ", "🙅‍♂ "} {
			if strings.HasPrefix(entry, marker) {
				entry = strings.TrimPrefix(entry, marker)
				break
			}
		}
		entry = strings.Join(strings.Fields(strings.ToValidUTF8(entry, "�")), " ")
		if entry == "" {
			continue
		}
		runes := []rune(entry)
		if len(runes) > archiveDescriptionPromptLimit {
			runes = runes[:archiveDescriptionPromptLimit]
		}
		if len(runes) > remaining {
			runes = runes[:remaining]
		}
		lines = append(lines, string(runes))
		remaining -= len(runes)
		if remaining == 0 {
			break
		}
	}
	return lines
}
