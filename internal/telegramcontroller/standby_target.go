package telegramcontroller

import (
	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"sort"
	"time"
)

// Popularity is per-node session count (caller excludes empty preparations).
// Ties use newest creation,
// then lexical path; provider uses node default, newest enabled, then inventory.
func standbyTarget(node domain.ComputerID, sessions []domain.Session, capabilities []sessioncreation.ProviderCapability, preferred domain.Provider) sessioncreation.Draft {
	type score struct {
		count  int
		latest time.Time
	}
	scores := map[string]score{}
	enabled := map[domain.Provider]bool{}
	for _, capability := range capabilities {
		enabled[capability.Provider] = capability.Installed && capability.Enabled
	}
	draft := sessioncreation.Draft{ComputerID: node}
	if enabled[preferred] {
		draft.Provider = preferred
	}
	var newest time.Time
	for _, session := range sessions {
		if session.ComputerID() != node {
			continue
		}
		entry := scores[session.Workdir()]
		entry.count++
		if session.CreatedAt().After(entry.latest) {
			entry.latest = session.CreatedAt()
		}
		scores[session.Workdir()] = entry
		if !enabled[preferred] && enabled[session.Provider()] && session.CreatedAt().After(newest) {
			draft.Provider = session.Provider()
			newest = session.CreatedAt()
		}
	}
	paths := make([]string, 0, len(scores))
	for path := range scores {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		a, b := scores[paths[i]], scores[paths[j]]
		if a.count != b.count {
			return a.count > b.count
		}
		if !a.latest.Equal(b.latest) {
			return a.latest.After(b.latest)
		}
		return paths[i] < paths[j]
	})
	if len(paths) > 0 {
		draft.Workdir = paths[0]
	}
	if draft.Provider == "" {
		for _, capability := range capabilities {
			if enabled[capability.Provider] {
				draft.Provider = capability.Provider
				break
			}
		}
	}
	return draft
}
