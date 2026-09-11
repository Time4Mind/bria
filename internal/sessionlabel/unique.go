// Package sessionlabel derives collision-free persisted session labels.
package sessionlabel

import (
	"fmt"
	"strings"

	"bria/internal/domain"
)

func Unique(sessions map[domain.IntentID]domain.Session, excluded domain.SessionID, requested string) string {
	used := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if session.ID() != excluded && session.Name() != "" {
			used[strings.ToLower(session.Name())] = struct{}{}
		}
	}
	for ordinal := 1; ordinal < 10000; ordinal++ {
		suffix := ""
		if ordinal > 1 {
			suffix = fmt.Sprintf("%d", ordinal)
		}
		runes := []rune(requested)
		limit := domain.MaxSessionNameRunes - len([]rune(suffix))
		if len(runes) > limit {
			runes = runes[:limit]
		}
		candidate := strings.TrimSpace(string(runes)) + suffix
		if _, exists := used[strings.ToLower(candidate)]; !exists {
			return candidate
		}
	}
	return requested
}
